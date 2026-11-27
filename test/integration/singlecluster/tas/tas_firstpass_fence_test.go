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
	"context"
	"sync/atomic"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("TopologyAwareScheduling: concurrent first-pass admission write", func() {
	const block = "cloud.example/block"
	const probeCondition = "TestConcurrentStatusUpdate"
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		nodes        []corev1.Node
	)

	newNode := func(name, cpu string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label("tas-node", "true").Label(block, "b1").Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(cpu),
				corev1.ResourcePods: resource.MustParse("10"),
			}).Ready().Obj()
	}

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-first-pass-")
		topology = utiltestingapi.MakeTopology("block-host").Levels(block, corev1.LabelHostname).Obj()
		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("tas-node", "true").TopologyName(topology.Name).Obj()
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).Obj()
		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		nodes = []corev1.Node{*newNode("first-1", "1"), *newNode("first-2", "1")}
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	createQueuesAndNodes := func() {
		ginkgo.GinkgoHelper()
		behavioral.MustCreate(ctx, k8sClient, topology)
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
	}

	ginkgo.DescribeTable("handles a Workload update before reserving quota", func(mergePatch bool, changeSpecAt int32) {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadRequestUseMergePatch, mergePatch)
		wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).RequiredTopologyRequest(block).
				Request(corev1.ResourceCPU, "1").Obj()).Obj()
		key := client.ObjectKeyFromObject(wl)

		var writes atomic.Int32
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithClientTransform(&config.Configuration{}, func(c client.Client) client.Client {
			return &admissionWriteClient{Client: c, beforeWrite: func(ctx context.Context, target types.NamespacedName) error {
				if target != key {
					return nil
				}
				write := writes.Add(1)
				if write != 1 && write != changeSpecAt {
					return nil
				}
				var latest kueue.Workload
				if err := k8sClient.Get(ctx, key, &latest); err != nil {
					return err
				}
				if write == changeSpecAt {
					latest.Spec.PodSets[0].TopologyRequest.Required = new(corev1.LabelHostname)
					return k8sClient.Update(ctx, &latest)
				}
				apimeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
					Type: probeCondition, Status: metav1.ConditionTrue, Reason: "Test",
					Message: "written before admission",
				})
				return k8sClient.Status().Update(ctx, &latest)
			}}
		}))

		createQueuesAndNodes()
		behavioral.MustCreate(ctx, k8sClient, wl)

		var latest kueue.Workload
		if changeSpecAt > 0 {
			// Neither existing node can fit both Pods on one hostname.
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, key, &latest)).To(gomega.Succeed())
				g.Expect(latest.Generation).To(gomega.BeNumerically(">", wl.Generation))
				g.Expect(latest.Spec.PodSets[0].TopologyRequest.Required).To(gomega.Equal(new(corev1.LabelHostname)))
				g.Expect(latest.Status.Admission).To(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(&latest)).To(gomega.BeFalse())
				var events corev1.EventList
				g.Expect(k8sClient.List(ctx, &events, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(events.Items).To(gomega.ContainElement(gomega.HaveField("Reason", "OutdatedScheduleCycle")))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			nodes = append(nodes, *newNode("first-3", "2"))
			integration.CreateNodesWithStatus(ctx, k8sClient, nodes[len(nodes)-1:])
		}

		behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, key)
		gomega.Expect(k8sClient.Get(ctx, key, &latest)).To(gomega.Succeed())
		gomega.Expect(writes.Load()).To(gomega.BeNumerically(">=", 2))
		if changeSpecAt > 0 {
			want := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
				Levels:  []string{corev1.LabelHostname},
				Domains: []utiltas.TopologyDomainAssignment{{Count: 2, Values: []string{"first-3"}}},
			})
			gomega.Expect(latest.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(want))
		} else {
			gomega.Expect(latest.Generation).To(gomega.Equal(wl.Generation))
			gomega.Expect(apimeta.IsStatusConditionTrue(latest.Status.Conditions, probeCondition)).To(gomega.BeTrue())
			var events corev1.EventList
			gomega.Expect(k8sClient.List(ctx, &events, client.InNamespace(ns.Name))).To(gomega.Succeed())
			gomega.Expect(events.Items).NotTo(gomega.ContainElement(gomega.HaveField("Reason", "OutdatedScheduleCycle")))
		}
	},
		ginkgo.Entry("with a changed spec and SSA", false, int32(1)),
		ginkgo.Entry("with a changed spec and Merge Patch", true, int32(1)),
		ginkgo.Entry("with a status-only update and SSA", false, int32(0)),
		ginkgo.Entry("with a status-only update and Merge Patch", true, int32(0)),
		ginkgo.Entry("with a spec change during the SSA retry", false, int32(2)),
		ginkgo.Entry("with a spec change during the Merge Patch retry", true, int32(2)),
	)
	ginkgo.DescribeTable("handles an elastic replacement's cluster during admission retry", func(mergePatch, conflictingCluster bool) {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadRequestUseMergePatch, mergePatch)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)
		const inheritedCluster = "worker-a"
		const otherCluster = "worker-b"
		replacementKey := types.NamespacedName{Namespace: ns.Name, Name: "replacement"}
		var bumped atomic.Bool
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithClientTransform(&config.Configuration{}, func(c client.Client) client.Client {
			return &admissionWriteClient{Client: c, beforeWrite: func(ctx context.Context, target types.NamespacedName) error {
				if target != replacementKey || !bumped.CompareAndSwap(false, true) {
					return nil
				}
				var latest kueue.Workload
				if err := k8sClient.Get(ctx, target, &latest); err != nil {
					return err
				}
				// Use the same admission field owner as MultiKueue.
				return workloadpatching.PatchAdmissionStatus(ctx, k8sClient, &latest, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
					if conflictingCluster {
						wl.Status.ClusterName = new(otherCluster)
					} else {
						wl.Status.NominatedClusterNames = []string{inheritedCluster}
					}
					return true, nil
				})
			}}
		}))
		createQueuesAndNodes()

		predecessor := utiltestingapi.MakeWorkload("predecessor", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
			Annotation(constants.ElasticJobAnnotation, "true").
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).UnconstrainedTopologyRequest().
				Request(corev1.ResourceCPU, "1").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, predecessor)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, predecessor)
		predecessorKey := client.ObjectKeyFromObject(predecessor)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, predecessorKey, predecessor)).To(gomega.Succeed())
			predecessor.Status.NominatedClusterNames = []string{inheritedCluster}
			g.Expect(k8sClient.Status().Update(ctx, predecessor)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, predecessorKey, predecessor)).To(gomega.Succeed())
			predecessor.Status.ClusterName = new(inheritedCluster)
			predecessor.Status.NominatedClusterNames = nil
			g.Expect(k8sClient.Status().Update(ctx, predecessor)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			snapshot, err := schedulerCache.Snapshot(ctx)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			info := snapshot.ClusterQueue(kueue.ClusterQueueReference(clusterQueue.Name)).Workloads[workload.Key(predecessor)]
			g.Expect(info).NotTo(gomega.BeNil())
			g.Expect(info.Obj.Status.ClusterName).To(gomega.Equal(new(inheritedCluster)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		replacement := utiltestingapi.MakeWorkload(replacementKey.Name, ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
			Annotation(constants.ElasticJobAnnotation, "true").
			Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(predecessor))).
			Annotation(kueue.WorkloadSliceNameAnnotation, predecessor.Name).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).UnconstrainedTopologyRequest().
				Request(corev1.ResourceCPU, "1").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, replacement)

		if conflictingCluster {
			gomega.Eventually(func(g gomega.Gomega) {
				var events corev1.EventList
				g.Expect(k8sClient.List(ctx, &events, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(events.Items).To(gomega.ContainElement(gomega.HaveField("Reason", "OutdatedScheduleCycle")))
				g.Expect(k8sClient.Get(ctx, replacementKey, replacement)).To(gomega.Succeed())
				g.Expect(replacement.Status.ClusterName).To(gomega.Equal(new(otherCluster)))
				g.Expect(replacement.Status.Admission).To(gomega.BeNil())
				g.Expect(k8sClient.Get(ctx, predecessorKey, predecessor)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(predecessor)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		} else {
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, replacementKey)
			gomega.Expect(k8sClient.Get(ctx, replacementKey, replacement)).To(gomega.Succeed())
			gomega.Expect(replacement.Status.ClusterName).To(gomega.Equal(new(inheritedCluster)))
			gomega.Expect(replacement.Status.NominatedClusterNames).To(gomega.BeEmpty())
		}
		gomega.Expect(bumped.Load()).To(gomega.BeTrue())
	},
		ginkgo.Entry("with a status-only conflict and SSA", false, false),
		ginkgo.Entry("with a status-only conflict and Merge Patch", true, false),
		ginkgo.Entry("with an incompatible cluster and SSA", false, true),
		ginkgo.Entry("with an incompatible cluster and Merge Patch", true, true),
	)
})
