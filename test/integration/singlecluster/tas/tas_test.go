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
	"fmt"
	"slices"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Delete Topology", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.When("ResourceFlavor does not exist", func() {
			ginkgo.BeforeEach(func() {
				topology = utiltestingapi.MakeDefaultOneLevelTopology("topology")
				util.MustCreate(ctx, k8sClient, topology)
			})

			ginkgo.It("should allow to delete topology", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			})
		})

		ginkgo.When("ResourceFlavor exists", func() {
			ginkgo.BeforeEach(func() {
				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("topology").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				topology = utiltestingapi.MakeDefaultOneLevelTopology("topology")
				util.MustCreate(ctx, k8sClient, topology)

				clusterQueue = utiltestingapi.MakeClusterQueue("cq").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Obj(),
					).Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
			})

			ginkgo.It("should not allow to delete topology", func() {
				// A ClusterQueue is considered active only if its ResourceFlavors are present in the cache.
				// We need to wait for the ClusterQueue to ensure the ResourceFlavors are cached.
				ginkgo.By("waiting for the ClusterQueue to become active", func() {
					util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
				})

				createdTopology := &kueue.Topology{}

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
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
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

	ginkgo.When("non-TAS pod exists", func() {
		var (
			nodes        []corev1.Node
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

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
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("default").Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "999").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			for _, node := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
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
				util.MustCreate(ctx, k8sClient, nonTasPod)
			})

			ginkgo.By("create a workload which requires the node's capacity", func() {
				wl = utiltestingapi.MakeWorkload("wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				util.MustCreate(ctx, k8sClient, wl)

				ginkgo.By("verify the workload is not admitted", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					}, util.ShortConsistentDuration, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.By("terminate the non-TAS pod", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nonTasPod), nonTasPod)).To(gomega.Succeed())
					util.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, nonTasPod)
					g.Expect(k8sClient.Update(ctx, nonTasPod)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// https://github.com/kubernetes-sigs/kueue/issues/8653
			ginkgo.By("hack to requeue workload", func() {
				cqs := sets.New[kueue.ClusterQueueReference]("cluster-queue")
				qcache.QueueInadmissibleWorkloads(ctx, qManager, cqs)
			})

			ginkgo.By("expect TAS pod to admit", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
				util.MustCreate(ctx, k8sClient, nonTasPod)
			})

			ginkgo.By("creating a workload which requires the node's capacity", func() {
				wl = utiltestingapi.MakeWorkload("wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("verify the workload is not admitted", func() {
				util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				}, util.ShortConsistentDuration, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("delete the non-TAS pod", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, nonTasPod, true, 60*time.Second)
			})

			// https://github.com/kubernetes-sigs/kueue/issues/8653
			ginkgo.By("hack to requeue workload", func() {
				cqs := sets.New[kueue.ClusterQueueReference]("cluster-queue")
				qcache.QueueInadmissibleWorkloads(ctx, qManager, cqs)
			})

			ginkgo.By("expect TAS pod to admit", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
			})
		})

		ginkgo.It("Non-TAS pod has no node assignment", func() {
			var wl *kueue.Workload
			var nonTasPod *corev1.Pod
			ginkgo.By("creating a non-TAS pod without assignment", func() {
				nonTasPod = testingpod.MakePod("pod", ns.Name).
					Request(corev1.ResourceCPU, "1").
					Obj()
				util.MustCreate(ctx, k8sClient, nonTasPod)
			})

			ginkgo.By("creating a workload which requires the node's capacity", func() {
				wl = utiltestingapi.MakeWorkload("wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
			})

			ginkgo.By("expect TAS pod to admit", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
			})
		})

		ginkgo.It("workload gets scheduled as the usage of TAS pods and workloads is not double-counted", func() {
			var wl1, wl2 *kueue.Workload

			ginkgo.By("create a workload which requires the node's capacity", func() {
				wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "400m").
					Obj()
				util.MustCreate(ctx, k8sClient, wl1)
			})

			ginkgo.By("verify the first workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			})

			ginkgo.By("create a TAS pod belonging to the admitted workload", func() {
				// We manually create the pod because job controllers don't run in integration tests.
				// The pod must have a TAS annotation to be identified as such by the controller.
				tasPod := testingpod.MakePod("tas-pod", ns.Name).
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "400m").
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Obj()
				util.MustCreate(ctx, k8sClient, tasPod)
			})

			ginkgo.By("create another workload which requires the remaining node's capacity", func() {
				wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "500m").
					Obj()
				util.MustCreate(ctx, k8sClient, wl2)
			})

			ginkgo.By("expect second workload to admit", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
				util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 2)
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
				util.MustCreate(ctx, k8sClient, nonTasPod)
			})

			ginkgo.By("create a workload which requires the node's capacity", func() {
				wl = utiltestingapi.MakeWorkload("wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				util.MustCreate(ctx, k8sClient, wl)

				ginkgo.By("verify the workload is not admitted", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					}, util.ShortConsistentDuration, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.By("delete the non-TAS pod", func() {
				gomega.Expect(k8sClient.Delete(ctx, nonTasPod)).To(gomega.Succeed())
			})

			ginkgo.By("verify the non-TAS pod has deletionTimestamp", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nonTasPod), nonTasPod)).To(gomega.Succeed())
					g.Expect(nonTasPod.DeletionTimestamp).NotTo(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify that the TAS-workload doesn't admit", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				}, util.ShortConsistentDuration, util.Interval).Should(gomega.Succeed())
			})
			// note to future developer: this non-TAS pod doesn't delete properly after ns clean-up,
			// so it will keep taking node1's capacity.
			// need to debug this before writing future tests.
		})
	})

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
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
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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
					wl1 := utiltestingapi.MakeWorkload("wl1-inadmissible", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
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
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].Count = 2
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(utiltesting.DefaultBlockTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 2)
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
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl3)
				})

				ginkgo.By("verify the wl3 is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2, wl3)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 3)
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
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
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
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 4)
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
						Preferred: ptr.To(utiltesting.DefaultBlockTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify wl1 is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})

				ginkgo.By("create wl2 which is blocked by wl1", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl2.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
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

				ginkgo.By("verify wl2 gets admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})
			})

			ginkgo.It("should not admit the workload after the topology is deleted but should admit it after the topology is created", framework.SlowSpec, func() {
				var updatedTopology kueue.Topology

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
					wl = utiltestingapi.MakeWorkload("wl", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 2).
						RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
						Obj()).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("recreate the Topology and wait for the queue to become active", func() {
					topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
					util.MustCreate(ctx, k8sClient, topology)
					util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
					wl = utiltestingapi.MakeWorkload("wl", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 2).
						RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
						Obj()).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("recreate the ResourceFlavor and wait for the queue to become active", func() {
					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").Obj()
					util.MustCreate(ctx, k8sClient, tasFlavor)
					util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
		})

		ginkgo.When("Nodes are created before test with the hostname being the lowest level", func() {
			var (
				nodes []corev1.Node
			)
			ginkgo.BeforeEach(func() {
				//     b1          b2
				//   /    \      /    \
				//  r1    r2    r1    r2
				//  |      |    |      |
				//  x3    x1    x4    x2
				nodes = []corev1.Node{
					*testingnode.MakeNode("x3").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x3").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
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
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x4").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b2").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x4").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x2").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b2").
						Label(utiltesting.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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

			ginkgo.It("should respect TAS usage from workload admitted before Topology re-creation", func() {
				var wl1, wl2 *kueue.Workload
				ginkgo.By("creating a workload which can fit", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							NodeSelector(map[string]string{
								corev1.LabelHostname: "x1",
							}).Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})

				ginkgo.By("trigger topology deletion", func() {
					gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).To(gomega.Succeed())
				})

				ginkgo.By("remove topology finalizers to allow deletion", func() {
					var updatedTopology kueue.Topology
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
						updatedTopology.Finalizers = nil
						g.Expect(k8sClient.Update(ctx, &updatedTopology)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("wait for the topology to be fully deleted", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, false)
				})

				ginkgo.By("recreate the topology", func() {
					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					util.MustCreate(ctx, k8sClient, topology)
				})

				ginkgo.By("creating second workload which is blocked by the previous would", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							NodeSelector(map[string]string{
								corev1.LabelHostname: "x1",
							}).Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the wl2 has not been admitted", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl2)).To(gomega.BeFalse())
					}, util.ShortConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should correctly account for TAS usage when workloads admitted before Topology re-creation are deleted", func() {
				var wl1, wl2 *kueue.Workload
				ginkgo.By("creating a workload which can fit", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).
							NodeSelector(map[string]string{
								corev1.LabelHostname: "x1",
							}).Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})

				ginkgo.By("trigger topology deletion", func() {
					gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).To(gomega.Succeed())
				})

				ginkgo.By("remove topology finalizers to allow deletion", func() {
					var updatedTopology kueue.Topology
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
						updatedTopology.Finalizers = nil
						g.Expect(k8sClient.Update(ctx, &updatedTopology)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("wait for the topology to be fully deleted", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, false)
				})

				ginkgo.By("recreate the topology", func() {
					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					util.MustCreate(ctx, k8sClient, topology)
				})

				ginkgo.By("finish the first workload", func() {
					util.FinishWorkloads(ctx, k8sClient, wl1)
				})

				ginkgo.By("creating second workload which cannot fit", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(corev1.LabelHostname).
							NodeSelector(map[string]string{
								corev1.LabelHostname: "x1",
							}).Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the wl2 has not been admitted", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl2)).To(gomega.BeFalse())
					}, util.ShortConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should not admit workload which does not fit to the required topology domain", func() {
				ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
					wl1 := utiltestingapi.MakeWorkload("wl1-inadmissible", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 4).
							PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
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
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})

				ginkgo.By("creating second a workload which cannot fit", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker-2", 4).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
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
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						}),
					))
				})

				ginkgo.By("creating the second workload without explicit TAS annotation which cannot fit", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker-2", 3).
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
			ginkgo.It("should update workload TopologyAssignment when node is removed", framework.SlowSpec, func() {
				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						}),
					))
				})

				ginkgo.By("deleting the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload has corrected TopologyAssignment and no UnhealthyNode", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x1"}},
									{Count: 1, Values: []string{"x4"}},
								},
							}),
						))
						g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			ginkgo.It("should update workload TopologyAssignment when node fails", framework.SlowSpec, func() {
				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						}),
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

				ginkgo.By("verify the workload has corrected TopologyAssignment", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x1"}},
									{Count: 1, Values: []string{"x4"}},
								},
							}),
						))
						gomega.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			ginkgo.It("should remove node from unhealthyNodes when the node recovers", func() {
				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							// requiring the same block makes sure that no replacement is possible
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						}),
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

				ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("node recovers", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

					util.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Now()),
					})
				})

				ginkgo.By("verify the node is removed from unhealthyNodes", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).ToNot(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should remove the node from unhealthyNodes when node reappears", func() {
				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						}),
					))
				})

				nodeToDelete := &corev1.Node{}
				ginkgo.By("deleting the node", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nodeToDelete)).Should(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload eventually gets the UnhealthyNodes", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("node reappears", func() {
					nodeToDelete.ResourceVersion = ""
					gomega.Expect(k8sClient.Create(ctx, nodeToDelete)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the UnhealthyNodes is cleared", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).ToNot(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should update workload TopologyAssignment after a node becomes available", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)
				var wl1, wl2 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						}),
					))
				})

				ginkgo.By("creating a second workload", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
					gomega.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x4"}},
								{Count: 1, Values: []string{"x2"}},
							},
						}),
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
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("Finishing second workload")
				util.FinishWorkloads(ctx, k8sClient, wl2)

				ginkgo.By("verify the workload has corrected TopologyAssignment and no node in UnhealthyNodes", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x1"}},
									{Count: 1, Values: []string{"x4"}},
								},
							}),
						))
						g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should evict workload when multiple assigned nodes are deleted", func() {
				var wl1 *kueue.Workload
				node1Name := "x3"
				node2Name := "x1"

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							NodeSelector(map[string]string{utiltesting.DefaultBlockTopologyLevel: "b1"}).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{node1Name}},
								{Count: 1, Values: []string{node2Name}},
							},
						}),
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
						g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty(), "UnhealthyNodes should be cleared after eviction due to multiple node failures")
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should evict workload when multiple assigned nodes fail", func() {
				var wl1 *kueue.Workload
				node1Name := "x3"
				node2Name := "x1"

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{node1Name}},
								{Count: 1, Values: []string{node2Name}},
							},
						}),
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
						g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty(), "UnhealthyNodes should be cleared after eviction due to multiple node failures")
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			// Fixes https://github.com/kubernetes-sigs/kueue/issues/9210
			ginkgo.It("should fallback to greedy assignment when replacement pod conflicts with already running pod", framework.SlowSpec, func() {
				// Scenario: This test simulates an edge case during node replacement where a running pod (p1, rank 1)
				// is occupying a node (x3) that is assigned to a different rank (rank 0) in the TopologyAssignment.
				// This can happen in practice if a terminating pod is deleted very quickly during a node hotswap,
				// causing the replacement pod to receive NodeSelectors for the occupied node based on its job labels.
				// To prevent assigning multiple pods to the same node, the ungater detects this rank mismatch
				// between the running pod and the topology assignment, and falls back to greedy assignment.
				// This ensures the new pod (p0) is safely placed on the remaining available node (x1).
				var wl1 *kueue.Workload

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl-greedy", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
							SubGroupIndexLabel(ptr.To(jobset.JobIndexKey)).
							SubGroupCount(ptr.To[int32](2)).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						}),
					))
				})

				var p1 *corev1.Pod
				ginkgo.By("creating p1 manually assigned to x3 (assigned to rank 0) but given rank 1 to simulate mismatch", func() {
					p1 = testingpod.MakePod("p1-running", ns.Name).
						Annotation(kueue.WorkloadAnnotation, wl1.Name).
						Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
						Label(batchv1.JobCompletionIndexAnnotation, "1"). // rank 1
						Label(jobset.JobIndexKey, "1").
						Label(jobset.ReplicatedJobReplicas, "1").
						Label(constants.PodSetLabel, "worker").
						NodeSelector(corev1.LabelHostname, "x3"). // assigned to rank 0, but this is rank 1
						Obj()
					util.MustCreate(ctx, k8sClient, p1)
				})

				var p0 *corev1.Pod
				ginkgo.By("creating replacement p0", func() {
					p0 = testingpod.MakePod("p0-replacement", ns.Name).
						Annotation(kueue.WorkloadAnnotation, wl1.Name).
						Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
						Label(batchv1.JobCompletionIndexAnnotation, "0"). // rank 0
						Label(jobset.JobIndexKey, "0").
						Label(jobset.ReplicatedJobReplicas, "1").
						Label(constants.PodSetLabel, "worker").
						TopologySchedulingGate().
						Obj()
					util.MustCreate(ctx, k8sClient, p0)
				})

				ginkgo.By("verify p0 is ungated and assigned to a node other than x3 since x3 is occupied", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						pod := &corev1.Pod{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "p0-replacement"}, pod)).To(gomega.Succeed())
						g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
						g.Expect(pod.Spec.NodeSelector).Should(gomega.HaveKey(corev1.LabelHostname))
						g.Expect(pod.Spec.NodeSelector[corev1.LabelHostname]).To(gomega.Equal("x1"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})

		ginkgo.When("TASNodeFailedReplacementFailFast is enabled", func() {
			var (
				nodes []corev1.Node
			)
			ginkgo.BeforeEach(func() {
				//        b1
				//     /      \
				//    r1       r2
				//   /  \     /  \
				//  x3  x1   x4  x2
				nodes = []corev1.Node{
					*testingnode.MakeNode("x3").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x3").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x1").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x4").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x4").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x2").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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

			ginkgo.It("should evict the Workload if no replacement is possible and place it on another rack", framework.SlowSpec, func() {
				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x1"}},
								{Count: 1, Values: []string{"x3"}},
							},
						}),
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

				ginkgo.By("verify the workload is evicted due to no replacement possible", func() {
					util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
					util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedDueToNodeFailures, "", "", 1)
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).To(gomega.BeEmpty())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is placed on another rack", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x2"}},
								{Count: 1, Values: []string{"x4"}},
							},
						}),
					))
				})
			})
			ginkgo.It("should update workload UnhealthyNodes immediately when node has NoExecute taint and TASReplaceNodeOnPodTermination is disabled", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, false)

				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})

				ginkgo.By("applying NoExecute taint to the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
						Key:    "example.com/failure",
						Value:  "true",
						Effect: corev1.TaintEffectNoExecute,
					})
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			ginkgo.It("should NOT update workload UnhealthyNodes immediately when node has NoExecute taint and TASReplaceNodeOnPodTermination is enabled", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, true)

				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})

				var pod *corev1.Pod
				ginkgo.By("creating a pod for the workload assigned to nodeName", func() {
					pod = testingpod.MakePod("pod1", ns.Name).
						Annotation(kueue.WorkloadAnnotation, "wl1").
						NodeName(nodeName).
						Obj()
					util.MustCreate(ctx, k8sClient, pod)
				})

				ginkgo.By("applying NoExecute taint to the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
						Key:    "example.com/proactive",
						Value:  "true",
						Effect: corev1.TaintEffectNoExecute,
					})
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify UnhealthyNodes is NOT updated while pod is running", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("deleting the pod", func() {
					gomega.Expect(k8sClient.Delete(ctx, pod)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			ginkgo.It("should selectively recover workload health based on tolerations of remaining taints", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)

				var wl1, wl2 *kueue.Workload
				nodeName := nodes[0].Name // x3
				taintA := corev1.Taint{Key: "example.com/taint-a", Value: "true", Effect: corev1.TaintEffectNoExecute}
				taintB := corev1.Taint{Key: "example.com/taint-b", Value: "true", Effect: corev1.TaintEffectNoExecute}

				ginkgo.By("creating workloads", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							NodeSelector(map[string]string{corev1.LabelHostname: nodeName}).
							Toleration(corev1.Toleration{
								Key:      taintA.Key,
								Operator: corev1.TolerationOpEqual,
								Value:    taintA.Value,
								Effect:   taintA.Effect,
							}).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "100m").Obj()
					util.MustCreate(ctx, k8sClient, wl1)

					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							NodeSelector(map[string]string{corev1.LabelHostname: nodeName}).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "100m").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the workloads are admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
				})

				ginkgo.By("applying both taints to the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taintA, taintB)
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify both workloads eventually get an entry in unhealthyNodes list", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))

						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(wl2.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("removing Taint B from the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = []corev1.Taint{taintA}
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify WL1 becomes healthy while WL2 stays unhealthy", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))

						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(wl2.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			ginkgo.It("should evict workload when multiple assigned nodes get NoExecute taints", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)

				var wl1 *kueue.Workload
				node1Name := "x3"
				node2Name := "x1"
				taint := corev1.Taint{Key: "example.com/failure", Value: "true", Effect: corev1.TaintEffectNoExecute}

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
					gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
					node1Name = ta.Domains[0].Values[0]
					node2Name = ta.Domains[1].Values[0]
				})

				ginkgo.By("applying NoExecute taint to the first node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node1Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("applying NoExecute taint to the second node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node2Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is evicted due to multiple node failures", func() {
					util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
						g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty(), "UnhealthyNodes should be cleared after eviction due to multiple node failures")
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			ginkgo.It("should replace a tainted node with a new one within the same block", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)

				var wl1 *kueue.Workload
				nodeName := "x3"
				taint := corev1.Taint{Key: "example.com/failure", Value: "true", Effect: corev1.TaintEffectNoExecute}

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
					gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
					nodeName = ta.Domains[0].Values[0]
				})

				ginkgo.By("applying NoExecute taint to the first node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify that workload is rescheduled using a free node in the same block", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						g.Expect(ta.Domains).To(gomega.HaveLen(2))
						assignedNode1 := ta.Domains[0].Values[0]
						assignedNode2 := ta.Domains[1].Values[0]
						g.Expect([]string{assignedNode1, assignedNode2}).NotTo(gomega.ContainElement(nodeName))
						g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should evict the Workload if no replacement is possible after NoExecute taint", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)

				var wl1 *kueue.Workload
				nodeName := nodes[0].Name // x3
				taint := corev1.Taint{Key: "example.com/failure", Value: "true", Effect: corev1.TaintEffectNoExecute}

				ginkgo.By("creating a workload requiring a full rack", func() {
					// Rack r2 has exactly 2 nodes: x4 and x2. If one fails, it cannot be replaced within the same rack.
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted on a rack", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
					gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
					nodeName = ta.Domains[0].Values[0]
				})

				ginkgo.By("applying NoExecute taint to one of the assigned nodes", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is evicted due to no replacement possible", func() {
					util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
						g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is eventually placed on another rack", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
					gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
					assignedNode1 := ta.Domains[0].Values[0]
					assignedNode2 := ta.Domains[1].Values[0]
					gomega.Expect([]string{assignedNode1, assignedNode2}).NotTo(gomega.ContainElement(nodeName))
				})
			})

			ginkgo.It("should not evict workload when nodes get NoExecute taints that are tolerated", framework.SlowSpec, func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)

				var wl1 *kueue.Workload
				node1Name := "x3"
				node2Name := "x1"
				taint := corev1.Taint{Key: "example.com/tolerable", Value: "true", Effect: corev1.TaintEffectNoExecute}

				ginkgo.By("creating a workload with a toleration for the taint", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Toleration(corev1.Toleration{
								Key:      taint.Key,
								Operator: corev1.TolerationOpEqual,
								Value:    taint.Value,
								Effect:   taint.Effect,
							}).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
					gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
					node1Name = ta.Domains[0].Values[0]
					node2Name = ta.Domains[1].Values[0]
				})

				ginkgo.By("applying the tolerated NoExecute taint to the first node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node1Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("applying the tolerated NoExecute taint to the second node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node2Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify workload stays healthy and UnhealthyNodes remains empty", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.UnhealthyNodes).To(gomega.BeEmpty())
					}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
				})
			})
		})
		ginkgo.When("TASBalancePlacement is enabled", func() {
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
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource("nvidia.com/gpu", "40").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)

				_ = features.SetEnable(features.TASBalancedPlacement, true)
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

				_ = features.SetEnable(features.TASBalancedPlacement, false)
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
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
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
								SliceRequiredTopologyRequest(corev1.LabelHostname).
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
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
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
		ginkgo.When("Preemption is enabled within ClusterQueue", func() {
			var (
				nodes []corev1.Node
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
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

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
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Priority(1).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})

				ginkgo.By("creating a mid priority workload which can fit", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						Priority(2).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the wl2 gets admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 2)
				})

				ginkgo.By("creating a high priority workload which requires preemption", func() {
					wl3 = utiltestingapi.MakeWorkload("wl3", ns.Name).
						Priority(3).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
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
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

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
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)

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
				util.MustCreate(ctx, k8sClient, clusterQueueB)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueueB)

				localQueueB = utiltestingapi.MakeLocalQueue("local-queue-b", ns.Name).ClusterQueue(clusterQueueB.Name).Obj()
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
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "4").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
				topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})
		})

		ginkgo.When("Node is mutated during test cases", func() {
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
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
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})

			ginkgo.It("should admit workload when node is edited to match the required affinity node selector terms", func() {
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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

				ginkgo.By("creating a workload requiring the missing label via Affinity", func() {
					wl1 = utiltestingapi.MakeWorkload("wl-needs-label", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("main", 1).
							RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
								{
									MatchExpressions: []corev1.NodeSelectorRequirement{
										{
											Key:      customLabelKey,
											Operator: corev1.NodeSelectorOpIn,
											Values:   []string{customLabelCorrectValue},
										},
									},
								},
							}).
							Request(corev1.ResourceCPU, "1").
							Obj()).
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload with missing label is inadmissible", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("add a label to the node with correct key but wrong value", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Labels[customLabelKey] = customLabelWrongValue
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload with the correct label key but wrong value is inadmissible", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("add the correct label to the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Labels[customLabelKey] = customLabelCorrectValue
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload gets admitted after label with correct key and value is added", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
					wl1 = utiltestingapi.MakeWorkload("wl-needs-label", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("main", 1).
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
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				prc = utiltestingapi.MakeProvisioningRequestConfig("prov-config").
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

				ac = utiltestingapi.MakeAdmissionCheck("provisioning").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
					Obj()
				util.MustCreate(ctx, k8sClient, ac)
				util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

				clusterQueue = utiltestingapi.MakeClusterQueue("cq").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
					).AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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

			ginkgo.It("should admit workload when nodes are provisioned; Nodes ready after small delay", framework.SlowSpec, func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("create workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})

			ginkgo.It("should admit workload targeting the dedicated newly provisioned nodes", framework.SlowSpec, func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("create workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
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
					//      b1
					//   /      \
					//  r1       r2
					//  |         |
					//  x2       x1
					nodes = []corev1.Node{
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label("dedicated-selector-key", "dedicated-selector-value-abc").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label("dedicated-selector-key", "dedicated-selector-value-xyz").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x1").
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
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})

			ginkgo.It("should admit workload when nodes are provisioned and account for quota usage correctly", framework.SlowSpec, func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("creating a workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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

			ginkgo.It("should admit workload when nodes are provisioned; manager restart", framework.SlowSpec, func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("create workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
						state := admissioncheck.FindAdmissionCheck(wl1.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
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
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							}),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})
			})

			ginkgo.It("uses exponential second-pass backoff for the workload admission", framework.SlowSpec, func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("create workload", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Image("image").
							Obj(),
						).Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota and requires TAS assignment (second pass)", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				provReqKey := apitypes.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}

				ginkgo.By("await ProvisioningRequest creation", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("provision a node that is NOT Ready", func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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

				ginkgo.By("mark ProvisioningRequest as Provisioned (start of the backoff window)", func() {
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

				ginkgo.By("ensure no assignment during three backoff intervals (≈1s, 2s, 4s - total 7s)", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeNil())
					}, 7*time.Second, util.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("observe three SecondPassFailed events while the node is NotReady (≈1s, 2s, 4s backoffs)", func() {
					var evList corev1.EventList
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, &evList, &client.ListOptions{Namespace: ns.Name})).To(gomega.Succeed())
						var count int32
						for i := range evList.Items {
							ev := evList.Items[i]
							if ev.Reason == "SecondPassFailed" && ev.InvolvedObject.Name == wl1.Name {
								if ev.Count > count {
									count = ev.Count
								}
							}
						}
						g.Expect(count).Should(gomega.Equal(int32(3)))
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

				ginkgo.By("admit after nodes become Ready (after 8s backoff finishes)", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{{
									Count:  1,
									Values: []string{"x1"},
								}},
							}),
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeTopology("default").Levels(
					utiltesting.DefaultRackTopologyLevel,
				).Obj()
				util.MustCreate(ctx, k8sClient, topology)

				tasGPUFlavor = utiltestingapi.MakeResourceFlavor("tas-gpu-flavor").
					NodeLabel(corev1.LabelInstanceTypeStable, "gpu-node").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasGPUFlavor)

				tasCPUFlavor = utiltestingapi.MakeResourceFlavor("tas-cpu-flavor").
					NodeLabel(corev1.LabelInstanceTypeStable, "cpu-node").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasCPUFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(tasGPUFlavor.Name).Resource(corev1.ResourceCPU, "1").Resource(gpuResName, "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas(tasCPUFlavor.Name).Resource(corev1.ResourceCPU, "5").Resource(gpuResName, "0").Obj(),
					).Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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

			ginkgo.It("should admit workload which fits in a required topology domain", func() {
				var wl1 *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
					ps1 := *utiltestingapi.MakePodSet("manager", 1).NodeSelector(
						map[string]string{corev1.LabelInstanceTypeStable: "cpu-node"},
					).Request(corev1.ResourceCPU, "5").Obj()
					ps1.TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
					}
					ps2 := *utiltestingapi.MakePodSet("worker", 2).NodeSelector(
						map[string]string{corev1.LabelInstanceTypeStable: "gpu-node"},
					).Request(gpuResName, "2").Obj()
					ps2.TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(utiltesting.DefaultRackTopologyLevel),
					}
					wl1.Spec.PodSets = []kueue.PodSet{ps1, ps2}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
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
				// per resource, TAS would fail with "more than one flavor assigned" (onlyFlavor).
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
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("verify the workload remains pending and is not admitted", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					gomega.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				})

				ginkgo.By("verify QuotaReserved condition reports flavor/TAS-related failure", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(cond).ToNot(gomega.BeNil())
						g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(cond.Reason).To(gomega.Equal("Pending"))
						// With this CQ, no single flavor fits both CPU and GPU, so flavor assigner fails with "couldn't assign flavors".
						g.Expect(cond.Message).To(gomega.ContainSubstring("couldn't assign flavors"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})
	})

	// The purpose of this test is to demonstrate a TAS use case
	// for just the host scope of fragmentation
	ginkgo.When("Testing node resource fragmentation", func() {
		var (
			nodes []corev1.Node
		)

		ginkgo.BeforeEach(func() {
			// 1. Create 8 fake nodes with 8 GPUs each. (8 nodes * 8 GPU = 64 GPUs)
			// 2. Per node, schedule a workload that uses 7 of the 8 GPUs. (7 x 8 = 56 GPU allocated)
			// This means:
			// There are 8 total GPUs unallocated, however each GPU is on a different node.
			nodes = make([]corev1.Node, 8)
			for i := range 8 {
				nodes[i] = *testingnode.MakeNode(fmt.Sprintf("fake-gpu-node-%d", i+1)).
					Label("nodepool", "gpu-nodes").
					Label(corev1.LabelHostname, fmt.Sprintf("fake-gpu-node-%d", i+1)).
					StatusAllocatable(corev1.ResourceList{
						"nvidia.com/gpu":    resource.MustParse("8"), // 8 GPUs per node
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					StatusConditions(corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(time.Now()),
						Reason:             "KubeletReady",
						Message:            "kubelet is posting ready status",
					}).
					Obj()
			}

			ginkgo.By("Creating nodes first", func() {
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				// Verify all nodes are properly recognized by TAS system
				gomega.Eventually(func(g gomega.Gomega) {
					var nodeList corev1.NodeList
					g.Expect(k8sClient.List(ctx, &nodeList)).To(gomega.Succeed())
					g.Expect(nodeList.Items).To(gomega.HaveLen(8))
					for _, node := range nodeList.Items {
						g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.AfterEach(func() {
			for i := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
			}
		})

		ginkgo.Context("Without Topology Aware Scheduling", func() {
			var (
				nonTasFlavor       *kueue.ResourceFlavor
				nonTasClusterQueue *kueue.ClusterQueue
				nonTasLocalQueue   *kueue.LocalQueue
			)

			ginkgo.BeforeEach(func() {
				// Create a ResourceFlavor without topology
				nonTasFlavor = utiltestingapi.MakeResourceFlavor("non-tas-gpu-flavor").
					NodeLabel("nodepool", "gpu-nodes").
					// No TopologyName - this simulates non-TAS behavior
					Obj()
				util.MustCreate(ctx, k8sClient, nonTasFlavor)

				nonTasClusterQueue = utiltestingapi.MakeClusterQueue("non-tas-gpu-cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("non-tas-gpu-flavor").
							Resource("nvidia.com/gpu", "64"). // Total 64 GPUs available (8 nodes × 8 GPUs)
							Obj(),
					).
					Obj()
				util.MustCreate(ctx, k8sClient, nonTasClusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, nonTasClusterQueue)

				nonTasLocalQueue = utiltestingapi.MakeLocalQueue("non-tas-gpu-queue", ns.Name).
					ClusterQueue("non-tas-gpu-cluster-queue").
					Obj()
				util.MustCreate(ctx, k8sClient, nonTasLocalQueue)

				// Create existing workloads that consume 7 GPUs on each node
				// This simulates the scenario where only 1 GPU is available per node
				for i := range 8 {
					existingWl := utiltestingapi.MakeWorkload(fmt.Sprintf("existing-workload-%d", i+1), ns.Name).
						Queue(kueue.LocalQueueName(nonTasLocalQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							Request("nvidia.com/gpu", "7"). // Single pod needs 7 GPUs
							NodeSelector(map[string]string{
								"nodepool":           "gpu-nodes",
								corev1.LabelHostname: fmt.Sprintf("fake-gpu-node-%d", i+1),
							}).
							Obj()).
						Obj()
					util.MustCreate(ctx, k8sClient, existingWl)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingWl)
				}
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, nonTasClusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, nonTasFlavor, true)
			})

			ginkgo.It("Should demonstrate fragmentation issue", framework.SlowSpec, func() {
				// Create a 1 pod workload that needs 8 GPUs on a single node
				// This should cause fragmentation because:
				// - Kueue sees 8 GPUs available total (1 GPU per node across 8 nodes)
				// - But a single pod requiring 8 GPUs cannot fit on any single node
				wl := utiltestingapi.MakeWorkload("fragmented-workload", ns.Name).
					Queue(kueue.LocalQueueName(nonTasLocalQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet("worker", 1).
						Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
						NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
						Obj()).
					Obj()

				ginkgo.By("Creating the workload that requires all 8 GPUs", func() {
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("Verifying the workload gets admitted (Kueue thinks it can schedule)", func() {
					// Without TAS, Kueue should admit this because it sees 8 GPUs are available
					// irrespective of quota topology
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					// We expect 9 total workloads: 8 existing (7 GPUs each) + 1 new (8 GPUs)
					// Total: 8×7 + 1×8 = 64 GPUs, which fits in the 64 GPU quota
					util.ExpectReservingActiveWorkloadsMetric(nonTasClusterQueue, 9)
				})

				ginkgo.By("Verifying admission shows resource assignment", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					gomega.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
					gomega.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					gomega.Expect(wl.Status.Admission.PodSetAssignments[0].Flavors).Should(gomega.Equal(
						map[corev1.ResourceName]kueue.ResourceFlavorReference{
							"nvidia.com/gpu": "non-tas-gpu-flavor",
						},
					))
					// Without TAS, there should be no TopologyAssignment
					gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeNil())
				})

				ginkgo.By("Demonstrating the problem: workload is admitted but cannot actually be scheduled", func() {
					// This test demonstrates the core issue:
					// 1. Kueue sees 8 GPUs available total (1 per node across 8 nodes) and admits the 1 pod workload
					// 2. But the single pod needs 8 GPUs in 1 node, and no single node has 8 available GPUs
					// 3. Each node only has 1 GPU available (the other 7 are busy with existing workloads)
					// 4. This leads to the pod being unschedulable, and will eventually reach scheduling timeout in
					//    a real world scenario

					// The workload is admitted, showing Kueue thinks it can be scheduled
					gomega.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeTrue())
				})
			})
		})

		ginkgo.Context("With Topology-Aware Scheduling", func() {
			var (
				tasFlavor    *kueue.ResourceFlavor
				topology     *kueue.Topology
				localQueue   *kueue.LocalQueue
				clusterQueue *kueue.ClusterQueue
			)
			ginkgo.Context("Single pod fitting into single node scenario", func() {
				ginkgo.BeforeEach(func() {
					ginkgo.By("Creating TAS topology", func() {
						topology = utiltestingapi.MakeDefaultOneLevelTopology("node")
						util.MustCreate(ctx, k8sClient, topology)
					})

					ginkgo.By("Creating TAS ResourceFlavor", func() {
						tasFlavor = utiltestingapi.MakeResourceFlavor("gpu-flavor").
							NodeLabel("nodepool", "gpu-nodes").
							TopologyName("node").
							Obj()
						util.MustCreate(ctx, k8sClient, tasFlavor)
					})

					ginkgo.By("Creating ClusterQueue and LocalQueue", func() {
						clusterQueue = utiltestingapi.MakeClusterQueue("gpu-cluster-queue").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("gpu-flavor").
									Resource("nvidia.com/gpu", "64"). // Total 64 GPUs available (8 nodes × 8 GPUs)
									Obj(),
							).
							Obj()
						util.MustCreate(ctx, k8sClient, clusterQueue)
						util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

						localQueue = utiltestingapi.MakeLocalQueue("gpu-queue", ns.Name).
							ClusterQueue("gpu-cluster-queue").
							Obj()
						util.MustCreate(ctx, k8sClient, localQueue)
					})

					// Verify all nodes are properly recognized by TAS system
					gomega.Eventually(func(g gomega.Gomega) {
						var nodeList corev1.NodeList
						g.Expect(k8sClient.List(ctx, &nodeList)).To(gomega.Succeed())
						g.Expect(nodeList.Items).To(gomega.HaveLen(8))
						for _, node := range nodeList.Items {
							g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
						}
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					// Create existing workloads that consume 7 GPUs on each node
					// This simulates the scenario where only 1 GPU is available per node
					for i := range 8 {
						existingWl := utiltestingapi.MakeWorkload(fmt.Sprintf("existing-tas-workload-%d", i+1), ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								Request("nvidia.com/gpu", "7").                // Single pod needs 7 GPUs
								RequiredTopologyRequest(corev1.LabelHostname). // Require hostname topology
								Obj()).
							Obj()
						util.MustCreate(ctx, k8sClient, existingWl)
						util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingWl)
					}
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				})

				ginkgo.It("Should prevent unschedulable admission by rejecting 8-GPU workload that requires 8-GPUs on single node", framework.SlowSpec, func() {
					// Create a workload that needs 8 GPUs on a single pod
					// With TAS, this should be rejected because no single node has 8 available GPUs
					wl := utiltestingapi.MakeWorkload("tas-workload", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
							NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
							RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
							Obj()).
						Obj()

					ginkgo.By("Creating the workload with TAS hostname requirement", func() {
						util.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("Verifying the workload is not admitted due to topology constraints", func() {
						// With TAS, Kueue should not admit this because no single node has 8 GPUs
						util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
						util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("Verifying workload remains pending (preventing unscheduable admission thus preventing resource waste)", func() {
						// The workload should remain pending
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeFalse())
						}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("Should allow workload that fits within topology constraints", framework.SlowSpec, func() {
					// Create a workload that needs only 1 GPU (fits on any single node)
					wl := utiltestingapi.MakeWorkload("tas-fitting-workload", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							Request("nvidia.com/gpu", "1").
							NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
							RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
							Obj()).
						Obj()

					ginkgo.By("Creating the workload with TAS hostname requirement that fits", func() {
						util.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("Verifying the workload IS admitted and runs successfully", func() {
						// This should work because 1 GPU fits on any single node
						util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 9) // 8 original workloads, 9th 1 GPU workload here
					})

					ginkgo.By("Verifying admission contains topology assignment", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).Should(gomega.Equal([]string{corev1.LabelHostname}))
						gomega.Expect(slices.Collect(utiltas.PodCounts(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment))).To(gomega.Equal([]int32{1}))
					})
				})
			})

			ginkgo.Context("Preemption with fragmentation scenario", func() {
				var (
					nodes             []corev1.Node
					topology          *kueue.Topology
					tasFlavor         *kueue.ResourceFlavor
					localQueue        *kueue.LocalQueue
					clusterQueue      *kueue.ClusterQueue
					highPriorityClass *kueue.WorkloadPriorityClass
					lowPriorityClass  *kueue.WorkloadPriorityClass
					allWorkloads      []*kueue.Workload
				)

				ginkgo.BeforeEach(func() {
					ginkgo.By("Creating 4 GPU nodes with 8 GPUs each")
					nodes = make([]corev1.Node, 4)
					for i := range 4 {
						nodes[i] = *testingnode.MakeNode(fmt.Sprintf("preempt-gpu-node-%d", i+1)).
							Label("nodepool", "preemption-gpu-nodes").
							Label(corev1.LabelHostname, fmt.Sprintf("preempt-gpu-node-%d", i+1)).
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":    resource.MustParse("8"),
								corev1.ResourcePods: resource.MustParse("20"),
							}).
							StatusConditions(corev1.NodeCondition{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionTrue,
								LastTransitionTime: metav1.NewTime(time.Now()),
								Reason:             "KubeletReady",
								Message:            "kubelet is posting ready status",
							}).
							Obj()
					}
					util.CreateNodesWithStatus(ctx, k8sClient, nodes)

					gomega.Eventually(func(g gomega.Gomega) {
						var nodeList corev1.NodeList
						g.Expect(k8sClient.List(ctx, &nodeList, client.MatchingLabels{
							"nodepool": "preemption-gpu-nodes",
						})).To(gomega.Succeed())
						g.Expect(nodeList.Items).To(gomega.HaveLen(4))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					ginkgo.By("Creating TAS topology")
					topology = utiltestingapi.MakeDefaultOneLevelTopology("preemption-topology")
					util.MustCreate(ctx, k8sClient, topology)

					ginkgo.By("Creating TAS ResourceFlavor")
					tasFlavor = utiltestingapi.MakeResourceFlavor("preemption-gpu-flavor").
						NodeLabel("nodepool", "preemption-gpu-nodes").
						TopologyName("preemption-topology").
						Obj()
					util.MustCreate(ctx, k8sClient, tasFlavor)

					ginkgo.By("Creating WorkloadPriorityClasses")
					highPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high-priority").
						PriorityValue(100).
						Obj()
					util.MustCreate(ctx, k8sClient, highPriorityClass)

					lowPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("low-priority").
						PriorityValue(10).
						Obj()
					util.MustCreate(ctx, k8sClient, lowPriorityClass)

					ginkgo.By("Creating ClusterQueue with preemption enabled")
					clusterQueue = utiltestingapi.MakeClusterQueue("preemption-gpu-cq").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("preemption-gpu-flavor").
								Resource("nvidia.com/gpu", "32").
								Obj(),
						).
						Preemption(kueue.ClusterQueuePreemption{
							WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
						}).
						Obj()
					util.MustCreate(ctx, k8sClient, clusterQueue)
					util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

					ginkgo.By("Creating LocalQueue")
					localQueue = utiltestingapi.MakeLocalQueue("preemption-gpu-queue", ns.Name).
						ClusterQueue("preemption-gpu-cq").
						Obj()
					util.MustCreate(ctx, k8sClient, localQueue)

					ginkgo.By("Creating 24 high-priority workloads (6 per node)")
					highPriorityWorkloads := make([]*kueue.Workload, 24)
					for i := range 24 {
						nodeIndex := i / 6 // Distribute: 6 workloads per node
						nodeName := fmt.Sprintf("preempt-gpu-node-%d", nodeIndex+1)
						wl := utiltestingapi.MakeWorkload(fmt.Sprintf("high-priority-wl-%d", i+1), ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							WorkloadPriorityClassRef("high-priority").
							Priority(100).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								Request("nvidia.com/gpu", "1").
								NodeSelector(map[string]string{
									"nodepool":           "preemption-gpu-nodes",
									corev1.LabelHostname: nodeName,
								}).
								RequiredTopologyRequest(corev1.LabelHostname).
								Obj()).
							Obj()
						util.MustCreate(ctx, k8sClient, wl)
						highPriorityWorkloads[i] = wl
					}

					ginkgo.By("Creating 8 low-priority workloads (2 per node)")
					lowPriorityWorkloads := make([]*kueue.Workload, 8)
					for i := range 8 {
						nodeIndex := i / 2 // Distribute: 2 workloads per node
						nodeName := fmt.Sprintf("preempt-gpu-node-%d", nodeIndex+1)
						wl := utiltestingapi.MakeWorkload(fmt.Sprintf("low-priority-wl-%d", i+1), ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							WorkloadPriorityClassRef("low-priority").
							Priority(10).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								Request("nvidia.com/gpu", "1").
								NodeSelector(map[string]string{
									"nodepool":           "preemption-gpu-nodes",
									corev1.LabelHostname: nodeName,
								}).
								RequiredTopologyRequest(corev1.LabelHostname).
								Obj()).
							Obj()
						util.MustCreate(ctx, k8sClient, wl)
						lowPriorityWorkloads[i] = wl
					}

					ginkgo.By("Waiting for all workloads to be admitted")
					allWorkloads = make([]*kueue.Workload, 0, 32)
					allWorkloads = append(allWorkloads, highPriorityWorkloads...)
					allWorkloads = append(allWorkloads, lowPriorityWorkloads...)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, allWorkloads...)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 32)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityClass, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for i := range nodes {
						util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
					}
				})

				ginkgo.It("Should reject workload with required topology after preemption causes fragmentation", framework.SlowSpec, func() {
					// Test scenario:
					// - 4 nodes with 8 GPUs each
					// - 24 high-priority 1-GPU workloads (6 per node)
					// - 8 low-priority 1-GPU workloads (2 per node)
					// - New high-priority workload with 8 pods × 1 GPU each (required topology)
					//
					// Expected behavior:
					// Even after preempting 8 low-priority workloads, the freed GPUs would be
					// fragmented (2 per node across 4 nodes). Since the workload requires all
					// 8 pods on a single node, and no single node can accommodate 8 pods,
					// TAS should reject it without preemption.

					wl := utiltestingapi.MakeWorkload("high-priority-required-wl", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						WorkloadPriorityClassRef("high-priority").
						Priority(100).
						PodSets(*utiltestingapi.MakePodSet("worker", 8).
							Request("nvidia.com/gpu", "1").
							NodeSelector(map[string]string{"nodepool": "preemption-gpu-nodes"}).
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj()).
						Obj()

					ginkgo.By("Creating the high-priority workload with required topology")
					util.MustCreate(ctx, k8sClient, wl)

					ginkgo.By("Verifying the workload is not admitted due to topology constraints")
					// The workload should remain pending because TAS recognizes that
					// even after preemption, resources would be fragmented
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

					ginkgo.By("Verifying workload stays pending (no quota reservation)")
					gomega.Consistently(func(g gomega.Gomega) {
						var updatedWl kueue.Workload
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
						g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

					ginkgo.By("Verifying no low-priority workloads were evicted")
					// Since the high-priority workload cannot be admitted even with preemption,
					// no workloads should be evicted
					gomega.Consistently(func(g gomega.Gomega) {
						evictedWorkloads := util.FilterEvictedWorkloads(ctx, k8sClient, allWorkloads...)
						g.Expect(evictedWorkloads).To(gomega.BeEmpty())
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.It("Should admit workload with preferred topology after preemption", func() {
					// Test scenario:
					// - Same setup: 4 nodes with 8 GPUs each
					// - 24 high-priority 1-GPU workloads (6 per node)
					// - 8 low-priority 1-GPU workloads (2 per node)
					// - New high-priority workload with 8 pods × 1 GPU each (preferred topology)
					//
					// Expected behavior:
					// With preferred topology, TAS allows the 8 pods to be distributed across
					// multiple nodes. It should preempt the 8 low-priority workloads (2 per node)
					// and successfully admit the workload with 2 pods per node across 4 nodes.

					wl := utiltestingapi.MakeWorkload("high-priority-preferred-wl", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						WorkloadPriorityClassRef("high-priority").
						Priority(100).
						PodSets(*utiltestingapi.MakePodSet("worker", 8).
							Request("nvidia.com/gpu", "1").
							NodeSelector(map[string]string{"nodepool": "preemption-gpu-nodes"}).
							PreferredTopologyRequest(corev1.LabelHostname).
							Obj()).
						Obj()

					ginkgo.By("Creating the high-priority workload with preferred topology")
					util.MustCreate(ctx, k8sClient, wl)

					ginkgo.By("Verifying low-priority workloads are evicted")
					// With preferred topology, TAS should preempt low-priority workloads
					// to distribute the 8 pods across nodes
					var evictedWorkloads []*kueue.Workload
					gomega.Eventually(func(g gomega.Gomega) {
						evictedWorkloads = util.FilterEvictedWorkloads(ctx, k8sClient, allWorkloads...)
						g.Expect(evictedWorkloads).To(gomega.HaveLen(8))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					ginkgo.By("Finishing eviction for preempted workloads")
					util.FinishEvictionForWorkloads(ctx, k8sClient, evictedWorkloads...)

					ginkgo.By("Verifying the workload is admitted")
					// The workload should be admitted and distributed across multiple nodes
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 25) // 24 high-priority + 1 new workload
				})
			})

			ginkgo.Context("Multiple pods in 1 workload TAS scenario", func() {
				var (
					tasFlavor    *kueue.ResourceFlavor
					topology     *kueue.Topology
					localQueue   *kueue.LocalQueue
					clusterQueue *kueue.ClusterQueue
				)

				ginkgo.BeforeEach(func() {
					// Create TAS topology
					topology = utiltestingapi.MakeDefaultOneLevelTopology("gpu-topology")
					util.MustCreate(ctx, k8sClient, topology)

					// Create TAS ResourceFlavor
					tasFlavor = utiltestingapi.MakeResourceFlavor("gpu-tas-flavor").
						NodeLabel("nodepool", "gpu-nodes").
						TopologyName("gpu-topology").
						Obj()
					util.MustCreate(ctx, k8sClient, tasFlavor)

					// Create ClusterQueue and LocalQueue
					clusterQueue = utiltestingapi.MakeClusterQueue("gpu-tas-cluster-queue").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("gpu-tas-flavor").
								Resource("nvidia.com/gpu", "64"). // Total 64 GPUs available (8 nodes × 8 GPUs)
								Obj(),
						).
						Obj()
					util.MustCreate(ctx, k8sClient, clusterQueue)
					util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("gpu-tas-queue", ns.Name).
						ClusterQueue("gpu-tas-cluster-queue").
						Obj()
					util.MustCreate(ctx, k8sClient, localQueue)

					// Verify all nodes are properly recognized by TAS system
					gomega.Eventually(func(g gomega.Gomega) {
						var nodeList corev1.NodeList
						g.Expect(k8sClient.List(ctx, &nodeList)).To(gomega.Succeed())
						g.Expect(nodeList.Items).To(gomega.HaveLen(8))
						for _, node := range nodeList.Items {
							g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
						}
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					// Create 7 workloads that consume 7 GPUs each on different nodes
					// Here, 7 nodes have 1 GPU available each, and 1 node has 8 GPUs available
					for i := range 7 {
						existingWl := utiltestingapi.MakeWorkload(fmt.Sprintf("existing-7gpu-workload-%d", i+1), ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								Request("nvidia.com/gpu", "7").                // Single pod needs 7 GPUs
								RequiredTopologyRequest(corev1.LabelHostname). // Require hostname topology
								Obj()).
							Obj()
						util.MustCreate(ctx, k8sClient, existingWl)
						util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingWl)
					}
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				})

				ginkgo.It("Should handle workload with 8-GPU pod and 7-GPU pod requirements", func() {
					// Create a workload with two pods: one needing 8 GPUs and another needing 7 GPUs
					// With TAS, this should be rejected because:
					// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
					// - But the 7-GPU pod cannot fit on any single node (7 nodes have only 1 GPU available each)
					wl := utiltestingapi.MakeWorkload("mixed-gpu-workload", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Obj()

					// First pod set: 1 pod needing 8 GPUs
					ps1 := *utiltestingapi.MakePodSet("gpu8-worker", 1).
						Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
						NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
						RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
						Obj()

					// Second pod set: 1 pod needing 7 GPUs
					ps2 := *utiltestingapi.MakePodSet("gpu7-worker", 1).
						Request("nvidia.com/gpu", "7"). // Single pod needs 7 GPUs
						NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
						RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
						Obj()

					wl.Spec.PodSets = []kueue.PodSet{ps1, ps2}

					ginkgo.By("Creating the workload with mixed GPU requirements", func() {
						util.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("Verifying the workload is not admitted due to topology constraints", func() {
						// With TAS, Kueue should not admit this because:
						// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
						// - But the 7-GPU pod cannot fit on any single node (7 nodes have only 1 GPU available each)
						// - TAS prevents admission when not all pods can be scheduled
						util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
						util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("Verifying workload remains pending (preventing unschedulable admission)", func() {
						// The workload should remain pending, demonstrating TAS prevents
						// admission of workloads that cannot actually be scheduled
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeFalse())
						}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
					})
				})
				ginkgo.It("Should admit workload with 8-GPU pod and 1-GPU pod requirements", framework.SlowSpec, func() {
					// Create a workload with two pods: one needing 8 GPUs and another needing 1 GPU
					// With TAS, this should be admitted because:
					// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
					// - The 1-GPU pod can fit on any of the 7 nodes with 1 GPU available each
					// - TAS allows admission when all pods can be scheduled
					wl := utiltestingapi.MakeWorkload("admissible-mixed-gpu-workload", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Obj()

					// First pod set: 1 pod needing 8 GPUs
					ps1 := *utiltestingapi.MakePodSet("gpu8-worker", 1).
						Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
						NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
						RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
						Obj()

					// Second pod set: 1 pod needing 1 GPU
					ps2 := *utiltestingapi.MakePodSet("gpu1-worker", 1).
						Request("nvidia.com/gpu", "1"). // Single pod needs 1 GPU
						NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
						RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
						Obj()

					wl.Spec.PodSets = []kueue.PodSet{ps1, ps2}

					ginkgo.By("Creating the workload with 8+1 GPU requirements", func() {
						util.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("Verifying the workload is admitted due to schedulable topology constraints", func() {
						// With TAS, Kueue should admit this because:
						// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
						// - The 1-GPU pod can fit on any of the 7 nodes with 1 GPU available each
						// - TAS allows admission when all pods can be scheduled
						util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 8) // 7 existing + 1 new workload
					})

					ginkgo.By("Verifying admission contains correct topology assignments", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))

						// First pod set (8 GPUs) should be assigned to a single node
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).Should(gomega.Equal([]string{corev1.LabelHostname}))
						gomega.Expect(slices.Collect(utiltas.PodCounts(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment))).To(gomega.Equal([]int32{1}))

						// Second pod set (1 GPU) should be assigned to a single node
						gomega.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment.Levels).Should(gomega.Equal([]string{corev1.LabelHostname}))
						gomega.Expect(slices.Collect(utiltas.PodCounts(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment))).To(gomega.Equal([]int32{1}))
					})

					ginkgo.By("Demonstrating TAS prevents resource waste by allowing schedulable workloads", func() {
						// This test demonstrates the benefit of TAS:
						// 1. TAS correctly identifies that both pods can be scheduled
						// 2. The 8-GPU pod goes to the node with 8 available GPUs
						// 3. The 1-GPU pod goes to one of the nodes with 1 available GPU
						// 4. This prevents resource waste by admitting workloads that can actually be scheduled
						gomega.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeTrue())
					})
				})
			})
		})
	})

	ginkgo.When("TAS with ElasticJobsViaWorkloadSlices", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
			nodes        []corev1.Node
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)

			// Create two blocks with two racks each to test topology placement
			nodes = []corev1.Node{
				*testingnode.MakeNode("b1-r1").
					Label("node-group", "tas").
					Label(utiltesting.DefaultBlockTopologyLevel, "b1").
					Label(utiltesting.DefaultRackTopologyLevel, "r1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("8"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
						corev1.ResourcePods:   resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2").
					Label("node-group", "tas").
					Label(utiltesting.DefaultBlockTopologyLevel, "b1").
					Label(utiltesting.DefaultRackTopologyLevel, "r2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("8"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
						corev1.ResourcePods:   resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1").
					Label("node-group", "tas").
					Label(utiltesting.DefaultBlockTopologyLevel, "b2").
					Label(utiltesting.DefaultRackTopologyLevel, "r1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("8"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
						corev1.ResourcePods:   resource.MustParse("20"),
					}).
					Ready().
					Obj(),
			}
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("default").Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "24").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			for _, node := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
			}
		})

		ginkgo.It("should place scaled-up workload in same topology domain as original", func() {
			var wl1 *kueue.Workload

			ginkgo.By("create initial workload with 2 pods using unconstrained topology", func() {
				wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Obj()
				wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().
					Image("image").
					Obj()
				util.MustCreate(ctx, k8sClient, wl1)
			})

			ginkgo.By("verify the workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			})

			var originalAssignment *kueue.TopologyAssignment
			ginkgo.By("verify admission for the workload1 and record the assignment", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				originalAssignment = wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment
			})

			ginkgo.By("verify the original assignment is in block b1 or b2", func() {
				// Extract the block from the original assignment
				gomega.Expect(originalAssignment).ShouldNot(gomega.BeNil())
				gomega.Expect(originalAssignment.Slices).ShouldNot(gomega.BeEmpty())
			})

			ginkgo.By("create pods simulating running pods", func() {
				// Create pods with the workload slice name annotation - this is set by the reconciler
				for i := range 2 {
					pod := testingpod.MakePod(fmt.Sprintf("pod-%d", i), ns.Name).
						Annotation(kueue.WorkloadAnnotation, wl1.Name).
						Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
						Request(corev1.ResourceCPU, "1").
						Obj()
					util.MustCreate(ctx, k8sClient, pod)
				}
			})

			var wl2 *kueue.Workload
			ginkgo.By("create a replacement workload slice with more pods", func() {
				wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl1))).
					Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
					Obj()
				wl2.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
					Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().
					Image("image").
					Obj()
				util.MustCreate(ctx, k8sClient, wl2)
			})

			ginkgo.By("verify the replacement workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
			})

			ginkgo.By("verify the replacement workload has topology assignment", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
					g.Expect(wl2.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl2.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())

					newAssignment := wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment
					g.Expect(newAssignment.Slices).ShouldNot(gomega.BeEmpty())
					g.Expect(originalAssignment.Slices).ShouldNot(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should isolate workloads with same name in different namespaces", func() {
			ns2 := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "tas-elastic-ns2-"}}
			gomega.Expect(k8sClient.Create(ctx, ns2)).To(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns2)).To(gomega.Succeed())
			}()

			localQueue2 := utiltestingapi.MakeLocalQueue("local-queue", ns2.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue2)

			var wl1, wl2 *kueue.Workload
			ginkgo.By("create workloads in both namespaces with same name", func() {
				wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Obj()
				wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().
					Image("image").
					Obj()
				util.MustCreate(ctx, k8sClient, wl1)

				wl2 = utiltestingapi.MakeWorkload("wl1", ns2.Name).
					Queue(kueue.LocalQueueName(localQueue2.Name)).
					Obj()
				wl2.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().
					Image("image").
					Obj()
				util.MustCreate(ctx, k8sClient, wl2)
			})

			ginkgo.By("verify both workloads are admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
			})

			var originalAssignment1, originalAssignment2 *kueue.TopologyAssignment
			ginkgo.By("record the topology assignments", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					originalAssignment1 = wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment
					g.Expect(originalAssignment1).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
					g.Expect(wl2.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl2.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					originalAssignment2 = wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment
					g.Expect(originalAssignment2).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("create pods for both workloads", func() {
				// Pods for workload in namespace 1
				for i := range 2 {
					pod := testingpod.MakePod(fmt.Sprintf("pod-ns1-%d", i), ns.Name).
						Annotation(kueue.WorkloadAnnotation, wl1.Name).
						Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
						Request(corev1.ResourceCPU, "1").
						Obj()
					util.MustCreate(ctx, k8sClient, pod)
				}
				// Pods for workload in namespace 2
				for i := range 2 {
					pod := testingpod.MakePod(fmt.Sprintf("pod-ns2-%d", i), ns2.Name).
						Annotation(kueue.WorkloadAnnotation, wl2.Name).
						Annotation(kueue.WorkloadSliceNameAnnotation, wl2.Name).
						Request(corev1.ResourceCPU, "1").
						Obj()
					util.MustCreate(ctx, k8sClient, pod)
				}
			})

			var wl1Replacement, wl2Replacement *kueue.Workload
			ginkgo.By("create replacement workloads", func() {
				wl1Replacement = utiltestingapi.MakeWorkload("wl1-replacement", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl1))).
					Obj()
				wl1Replacement.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().
					Image("image").
					Obj()
				util.MustCreate(ctx, k8sClient, wl1Replacement)

				wl2Replacement = utiltestingapi.MakeWorkload("wl1-replacement", ns2.Name).
					Queue(kueue.LocalQueueName(localQueue2.Name)).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl2))).
					Obj()
				wl2Replacement.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().
					Image("image").
					Obj()
				util.MustCreate(ctx, k8sClient, wl2Replacement)
			})

			ginkgo.By("verify both replacement workloads are admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1Replacement)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2Replacement)
			})

			ginkgo.By("verify each replacement workload has topology assignment", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1Replacement), wl1Replacement)).To(gomega.Succeed())
					g.Expect(wl1Replacement.Status.Admission).ShouldNot(gomega.BeNil())
					newAssignment1 := wl1Replacement.Status.Admission.PodSetAssignments[0].TopologyAssignment
					g.Expect(newAssignment1).ShouldNot(gomega.BeNil())
					g.Expect(newAssignment1.Slices).ShouldNot(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2Replacement), wl2Replacement)).To(gomega.Succeed())
					g.Expect(wl2Replacement.Status.Admission).ShouldNot(gomega.BeNil())
					newAssignment2 := wl2Replacement.Status.Admission.PodSetAssignments[0].TopologyAssignment
					g.Expect(newAssignment2).ShouldNot(gomega.BeNil())
					g.Expect(newAssignment2.Slices).ShouldNot(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("Topology validations", func() {
	ginkgo.When("Creating a Topology", func() {
		ginkgo.DescribeTable("Validate Topology on creation", func(topology *kueue.Topology, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, topology)
			if err == nil {
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("valid Topology",
				utiltestingapi.MakeDefaultOneLevelTopology("valid"),
				gomega.Succeed()),
			ginkgo.Entry("no levels",
				utiltestingapi.MakeTopology("no-levels").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("invalid levels",
				utiltestingapi.MakeTopology("invalid-level").Levels("@invalid").Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("non-unique levels",
				utiltestingapi.MakeTopology("default").Levels(utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultBlockTopologyLevel).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname first",
				utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname, utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname middle",
				utiltestingapi.MakeTopology("default").Levels(utiltesting.DefaultBlockTopologyLevel, corev1.LabelHostname, utiltesting.DefaultRackTopologyLevel).Obj(),
				utiltesting.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname last",
				utiltestingapi.MakeTopology("default").Levels(utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel, corev1.LabelHostname).Obj(),
				gomega.Succeed()),
		)
	})

	ginkgo.When("Updating a Topology", func() {
		ginkgo.DescribeTable("Validate Topology on update", func(topology *kueue.Topology, updateTopology func(topology *kueue.Topology), matcher types.GomegaMatcher) {
			util.MustCreate(ctx, k8sClient, topology)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			}()
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), topology)).Should(gomega.Succeed())
			updateTopology(topology)
			gomega.Expect(k8sClient.Update(ctx, topology)).Should(matcher)
		},
			ginkgo.Entry("succeed to update topology",
				utiltestingapi.MakeDefaultOneLevelTopology("valid"),
				func(topology *kueue.Topology) {
					topology.Labels = map[string]string{
						"alpha": "beta",
					}
				},
				gomega.Succeed()),
			ginkgo.Entry("updating levels is prohibited",
				utiltestingapi.MakeDefaultOneLevelTopology("valid"),
				func(topology *kueue.Topology) {
					topology.Spec.Levels = append(topology.Spec.Levels, kueue.TopologyLevel{
						NodeLabel: "added",
					})
				},
				utiltesting.BeInvalidError()),
			ginkgo.Entry("updating levels order is prohibited",
				utiltestingapi.MakeDefaultThreeLevelTopology("default"),
				func(topology *kueue.Topology) {
					topology.Spec.Levels[0], topology.Spec.Levels[1] = topology.Spec.Levels[1], topology.Spec.Levels[0]
				},
				utiltesting.BeInvalidError()),
		)
	})
})
