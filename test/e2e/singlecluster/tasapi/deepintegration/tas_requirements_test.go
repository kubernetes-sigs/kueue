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

package deepintegration

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("TopologyAwareScheduling Multi-Level and Mixed Requirements", ginkgo.Label(e2e.Shard1, "area:tas", "feature:was_deepintegration"), func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-req-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("tas-flavor").
					Resource(corev1.ResourceCPU, "16").
					Resource(extraResource, "16").
					Obj(),
			).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllJobSetsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should admit a JobSet with 3-slice-level required topology alongside multiple PodSets and PodSet Groups", func() {
		threeLayerConstraints := fmt.Sprintf(
			`[{"topology":%q,"size":4},{"topology":%q,"size":2},{"topology":%q,"size":1}]`,
			utiltesting.DefaultBlockTopologyLevel,
			utiltesting.DefaultRackTopologyLevel,
			corev1.LabelHostname,
		)

		sampleJobSet := testingjobset.MakeJobSet("three-slice-multips-jobset", ns.Name).
			Queue(localQueue.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "sliced",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 4,
					Completions: 4,
					PodAnnotations: map[string]string{
						kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: threeLayerConstraints,
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "leader",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						kueue.PodSetGroupName:                  "same-group",
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "workers",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 3,
					Completions: 3,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						kueue.PodSetGroupName:                  "same-group",
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "standalone",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 2,
					Completions: 2,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultRackTopologyLevel,
					},
				},
			).
			RequestAndLimit("sliced", extraResource, "1").
			RequestAndLimit("leader", corev1.ResourceCPU, "100m").
			RequestAndLimit("workers", extraResource, "1").
			RequestAndLimit("standalone", corev1.ResourceCPU, "100m").
			Obj()

		behavioral.MustCreate(ctx, k8sClient, sampleJobSet)

		wlLookupKey := types.NamespacedName{Name: jobset.GetWorkloadNameForJobSet(sampleJobSet.Name, sampleJobSet.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await admission of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		const totalPods = 10
		ginkgo.By("ensure all pods are created, scheduled, and running", func() {
			e2e.ExpectPodsScheduledAndRunning(ctx, k8sClient, ns.Name, totalPods)
		})

		ginkgo.By("verify topology placement of 3-slice-level pods and grouped pods", func() {
			pods := &corev1.PodList{}
			gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())

			blockByNode := e2e.GetTopologyDomainByNode(ctx, k8sClient, utiltesting.DefaultBlockTopologyLevel)
			rackByNode := e2e.GetTopologyDomainByNode(ctx, k8sClient, utiltesting.DefaultRackTopologyLevel)

			slicedNodeByIndex := make(map[string]string, 4)
			var groupBlocks []string
			var standaloneRacks []string
			for _, pod := range pods.Items {
				rjName := pod.Labels[jobsetapi.ReplicatedJobNameKey]
				switch rjName {
				case "sliced":
					idx := pod.Labels[batchv1.JobCompletionIndexAnnotation]
					slicedNodeByIndex[idx] = pod.Spec.NodeName
				case "leader", "workers":
					groupBlocks = append(groupBlocks, blockByNode[pod.Spec.NodeName])
				case "standalone":
					standaloneRacks = append(standaloneRacks, rackByNode[pod.Spec.NodeName])
				}
			}

			gomega.Expect(slicedNodeByIndex).To(gomega.HaveLen(4))
			// All 4 sliced pods must be in the same block (slice size 4 at block level).
			slicedBlock := blockByNode[slicedNodeByIndex["0"]]
			for _, nodeName := range slicedNodeByIndex {
				gomega.Expect(blockByNode[nodeName]).To(gomega.Equal(slicedBlock))
			}
			// Each rack slice of size 2 must be in the same rack on distinct nodes.
			gomega.Expect(rackByNode[slicedNodeByIndex["0"]]).To(gomega.Equal(rackByNode[slicedNodeByIndex["1"]]))
			gomega.Expect(slicedNodeByIndex["0"]).NotTo(gomega.Equal(slicedNodeByIndex["1"]))
			gomega.Expect(rackByNode[slicedNodeByIndex["2"]]).To(gomega.Equal(rackByNode[slicedNodeByIndex["3"]]))
			gomega.Expect(slicedNodeByIndex["2"]).NotTo(gomega.Equal(slicedNodeByIndex["3"]))

			// All pods in "same-group" must share the same block.
			gomega.Expect(groupBlocks).To(gomega.HaveLen(4))
			for _, b := range groupBlocks {
				gomega.Expect(b).To(gomega.Equal(groupBlocks[0]))
			}

			// All standalone pods must share the same rack.
			gomega.Expect(standaloneRacks).To(gomega.HaveLen(2))
			gomega.Expect(standaloneRacks[0]).To(gomega.Equal(standaloneRacks[1]))
		})

		ginkgo.By(fmt.Sprintf("verify workload %q finishes", wlLookupKey), func() {
			behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
		})
	})

	ginkgo.It("Should admit a JobSet with 2-slice-level and single-level required topology alongside multiple PodSets and PodSet Groups", func() {
		twoLayerSliceConstraints := fmt.Sprintf(
			`[{"topology":%q,"size":2},{"topology":%q,"size":1}]`,
			utiltesting.DefaultRackTopologyLevel,
			corev1.LabelHostname,
		)

		sampleJobSet := testingjobset.MakeJobSet("two-slice-plus-single-jobset", ns.Name).
			Queue(localQueue.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "leader-1",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						kueue.PodSetGroupName:                  "group-1",
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "workers-1",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 4,
					Completions: 4,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation:                 utiltesting.DefaultBlockTopologyLevel,
						kueue.PodSetGroupName:                                  "group-1",
						kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: twoLayerSliceConstraints,
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "leader-2",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						kueue.PodSetGroupName:                  "group-2",
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "workers-2",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 3,
					Completions: 3,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultBlockTopologyLevel,
						kueue.PodSetGroupName:                  "group-2",
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "standalone",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 2,
					Completions: 2,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: utiltesting.DefaultRackTopologyLevel,
					},
				},
			).
			RequestAndLimit("leader-1", corev1.ResourceCPU, "100m").
			RequestAndLimit("workers-1", extraResource, "1").
			RequestAndLimit("leader-2", corev1.ResourceCPU, "100m").
			RequestAndLimit("workers-2", extraResource, "1").
			RequestAndLimit("standalone", corev1.ResourceCPU, "100m").
			Obj()

		behavioral.MustCreate(ctx, k8sClient, sampleJobSet)

		wlLookupKey := types.NamespacedName{Name: jobset.GetWorkloadNameForJobSet(sampleJobSet.Name, sampleJobSet.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await admission of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		const totalPods = 11
		ginkgo.By("ensure all pods are created, scheduled, and running", func() {
			e2e.ExpectPodsScheduledAndRunning(ctx, k8sClient, ns.Name, totalPods)
		})

		ginkgo.By("verify topology placement of group-1 and group-2 pods", func() {
			pods := &corev1.PodList{}
			gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())

			blockByNode := e2e.GetTopologyDomainByNode(ctx, k8sClient, utiltesting.DefaultBlockTopologyLevel)
			rackByNode := e2e.GetTopologyDomainByNode(ctx, k8sClient, utiltesting.DefaultRackTopologyLevel)

			workers1NodeByIndex := make(map[string]string, 4)
			var leader1Block string
			var group2Blocks []string
			var standaloneRacks []string
			for _, pod := range pods.Items {
				rjName := pod.Labels[jobsetapi.ReplicatedJobNameKey]
				switch rjName {
				case "leader-1":
					leader1Block = blockByNode[pod.Spec.NodeName]
				case "workers-1":
					idx := pod.Labels[batchv1.JobCompletionIndexAnnotation]
					workers1NodeByIndex[idx] = pod.Spec.NodeName
				case "leader-2", "workers-2":
					group2Blocks = append(group2Blocks, blockByNode[pod.Spec.NodeName])
				case "standalone":
					standaloneRacks = append(standaloneRacks, rackByNode[pod.Spec.NodeName])
				}
			}

			gomega.Expect(workers1NodeByIndex).To(gomega.HaveLen(4))
			for _, nodeName := range workers1NodeByIndex {
				gomega.Expect(blockByNode[nodeName]).To(gomega.Equal(leader1Block))
			}
			gomega.Expect(rackByNode[workers1NodeByIndex["0"]]).To(gomega.Equal(rackByNode[workers1NodeByIndex["1"]]))
			gomega.Expect(workers1NodeByIndex["0"]).NotTo(gomega.Equal(workers1NodeByIndex["1"]))
			gomega.Expect(rackByNode[workers1NodeByIndex["2"]]).To(gomega.Equal(rackByNode[workers1NodeByIndex["3"]]))
			gomega.Expect(workers1NodeByIndex["2"]).NotTo(gomega.Equal(workers1NodeByIndex["3"]))

			gomega.Expect(group2Blocks).To(gomega.HaveLen(4))
			for _, b := range group2Blocks {
				gomega.Expect(b).To(gomega.Equal(group2Blocks[0]))
			}

			gomega.Expect(standaloneRacks).To(gomega.HaveLen(2))
			gomega.Expect(standaloneRacks[0]).To(gomega.Equal(standaloneRacks[1]))
		})

		ginkgo.By(fmt.Sprintf("verify workload %q finishes", wlLookupKey), func() {
			behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
		})
	})

	ginkgo.It("Should admit a JobSet with mixed PodSets where only one PodSet has TAS topology annotations", func() {
		sampleJobSet := testingjobset.MakeJobSet("mixed-podsets-jobset", ns.Name).
			Queue(localQueue.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "rj1",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					PodAnnotations: map[string]string{
						kueue.PodSetRequiredTopologyAnnotation: corev1.LabelHostname,
					},
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "rj2",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				},
			).
			RequestAndLimit("rj1", extraResource, "1").
			RequestAndLimit("rj2", extraResource, "1").
			Obj()

		ginkgo.By("Creating the JobSet", func() {
			behavioral.MustCreate(ctx, k8sClient, sampleJobSet)
		})

		ginkgo.By("waiting for the JobSet to be unsuspended", func() {
			jobSetKey := client.ObjectKeyFromObject(sampleJobSet)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobSetKey, sampleJobSet)).To(gomega.Succeed())
				g.Expect(sampleJobSet.Spec.Suspend).Should(gomega.Equal(new(false)))
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the JobSet has nodeSelector set", func() {
			gomega.Expect(sampleJobSet.Spec.ReplicatedJobs[0].Template.Spec.Template.Spec.NodeSelector).To(gomega.Equal(
				map[string]string{
					tasNodeGroupLabel: instanceType,
				},
			))
		})

		wlLookupKey := types.NamespacedName{Name: jobset.GetWorkloadNameForJobSet(sampleJobSet.Name, sampleJobSet.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify PodSetAssignments", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
		})

		ginkgo.By("ensure all pods are created, scheduled, and running", func() {
			e2e.ExpectPodsScheduledAndRunning(ctx, k8sClient, ns.Name, 2)
		})

		ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
			behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
		})
	})

	ginkgo.It("Should not admit a JobSet with 2-slice-level required topology when neither level alone is the bottleneck", func() {
		type nodeTopologyConfig struct {
			name     string
			block    string
			rack     string
			capacity string
		}
		defaultNodeConfigs := []nodeTopologyConfig{
			{name: "kind-worker", block: "b1", rack: "r1", capacity: "1"},
			{name: "kind-worker2", block: "b1", rack: "r1", capacity: "1"},
			{name: "kind-worker3", block: "b1", rack: "r2", capacity: "1"},
			{name: "kind-worker4", block: "b1", rack: "r2", capacity: "1"},
			{name: "kind-worker5", block: "b2", rack: "r1", capacity: "1"},
			{name: "kind-worker6", block: "b2", rack: "r1", capacity: "1"},
			{name: "kind-worker7", block: "b2", rack: "r2", capacity: "1"},
			{name: "kind-worker8", block: "b2", rack: "r2", capacity: "1"},
		}
		// Custom topology: A0[2], A1[2], B0[2], C0[2], D0[1], D1[1], D2[1], D3[1].
		// - Block-only slices of size 4 would fit on blocks A (4 GPUs) and D (4 GPUs).
		// - Rack-only slices of size 2 would fit on racks A0, A1, B0, C0 (2 GPUs each).
		// - Combined 2-slice-level (block size 4 + rack size 2) cannot fit because only
		//   block A has two racks of capacity >= 2; block D's 4 GPUs are spread across 4 racks of 1 GPU.
		customNodeConfigs := []nodeTopologyConfig{
			{name: "kind-worker", block: "A", rack: "r0", capacity: "2"},
			{name: "kind-worker2", block: "A", rack: "r1", capacity: "2"},
			{name: "kind-worker3", block: "B", rack: "r0", capacity: "2"},
			{name: "kind-worker4", block: "C", rack: "r0", capacity: "2"},
			{name: "kind-worker5", block: "D", rack: "r0", capacity: "1"},
			{name: "kind-worker6", block: "D", rack: "r1", capacity: "1"},
			{name: "kind-worker7", block: "D", rack: "r2", capacity: "1"},
			{name: "kind-worker8", block: "D", rack: "r3", capacity: "1"},
		}

		applyNodeConfigs := func(configs []nodeTopologyConfig) {
			for _, cfg := range configs {
				gomega.Eventually(func(g gomega.Gomega) {
					node := &corev1.Node{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cfg.name}, node)).To(gomega.Succeed())
					g.Expect(clientutil.Patch(ctx, k8sClient, node, func() (bool, error) {
						node.Labels[utiltesting.DefaultBlockTopologyLevel] = cfg.block
						node.Labels[utiltesting.DefaultRackTopologyLevel] = cfg.rack
						return true, nil
					})).To(gomega.Succeed())
					g.Expect(clientutil.PatchStatus(ctx, k8sClient, node, func() (bool, error) {
						qty := resource.MustParse(cfg.capacity)
						node.Status.Capacity[extraResource] = qty
						node.Status.Allocatable[extraResource] = qty
						return true, nil
					})).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
		}

		ginkgo.DeferCleanup(func() {
			applyNodeConfigs(defaultNodeConfigs)
		})
		applyNodeConfigs(customNodeConfigs)

		twoLayerConstraints := fmt.Sprintf(
			`[{"topology":%q,"size":4},{"topology":%q,"size":2}]`,
			utiltesting.DefaultBlockTopologyLevel,
			utiltesting.DefaultRackTopologyLevel,
		)

		sampleJobSet := testingjobset.MakeJobSet("two-slice-negative-jobset", ns.Name).
			Queue(localQueue.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "workers",
					Image:       e2e.GetAgnHostImage(),
					Args:        e2e.BehaviorExitFast,
					Replicas:    1,
					Parallelism: 8,
					Completions: 8,
					PodAnnotations: map[string]string{
						kueue.PodSetSliceRequiredTopologyConstraintsAnnotation: twoLayerConstraints,
					},
				},
			).
			RequestAndLimit("workers", extraResource, "1").
			Obj()

		behavioral.MustCreate(ctx, k8sClient, sampleJobSet)

		wlLookupKey := types.NamespacedName{Name: jobset.GetWorkloadNameForJobSet(sampleJobSet.Name, sampleJobSet.UID), Namespace: ns.Name}
		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await creation of workload %q and verify simulation failure", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
				cond := apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond).Should(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: kueue.WorkloadQuotaReservedReasonSimulationFailed,
				}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message", "ObservedGeneration")))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By(fmt.Sprintf("verify workload %q consistently remains unadmitted", wlLookupKey), func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
