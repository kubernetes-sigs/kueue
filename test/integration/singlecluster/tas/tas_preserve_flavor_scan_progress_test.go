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
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util"
)

// equalPriority is shared by both Workloads in these specs so that the
// WithinClusterQueue: LowerPriority policy finds no preemption victim, as happens in a
// ClusterQueue where every Workload runs at the same priority.
const equalPriority = 100

// These specs cover the PreserveFlavorScanProgress gate end to end: a Workload whose TAS
// placement fails on the flavor selected by quota must still reach another flavor of the
// ResourceGroup and be admitted there.
var _ = ginkgo.Describe("Topology Aware Scheduling preserving flavor scan progress", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		nodes        []corev1.Node
		topology     *kueue.Topology
		tasFlavor1   *kueue.ResourceFlavor
		tasFlavor2   *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		// Fair sharing on, so the fair-share entry iterator and preemption path drive
		// this spec rather than the classical ones.
		fwk.StartManager(ctx, cfg, managerSetupWithConfig(&config.Configuration{
			FairSharing: &config.FairSharing{},
		}))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-preserve-flavor-scan-")

		// One node per flavor. The blocker Workload fills the flavor-1 node, so the
		// second Workload can only be placed on the flavor-2 node.
		nodes = []corev1.Node{
			*testingnode.MakeNode("preserve-f1").
				Label("node-group", "tas").
				Label("tas-flavor", "f1").
				Label(corev1.LabelHostname, "preserve-f1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("2"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj(),
			*testingnode.MakeNode("preserve-f2").
				Label("node-group", "tas").
				Label("tas-flavor", "f2").
				Label(corev1.LabelHostname, "preserve-f2").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("2"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor1 = utiltestingapi.MakeResourceFlavor("tas-flavor-1").
			NodeLabel("node-group", "tas").
			NodeLabel("tas-flavor", "f1").
			TopologyName("default").Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor1)

		tasFlavor2 = utiltestingapi.MakeResourceFlavor("tas-flavor-2").
			NodeLabel("node-group", "tas").
			NodeLabel("tas-flavor", "f2").
			TopologyName("default").Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor2)

		// Quota on each flavor exceeds what its single node can host, so flavor-1 keeps
		// looking admissible to the quota-only flavor selection after its node is full.
		//
		// The ClusterQueue sets two preemption policies. Neither policy offers
		// flavor-1 a way forward here: the two Workloads share a priority, so
		// WithinClusterQueue: LowerPriority finds no victim in the ClusterQueue, and
		// no other ClusterQueue in the Cohort is borrowing, so ReclaimWithinCohort
		// has nothing to reclaim. That combination leaves a Workload pinned to one
		// flavor.
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			Cohort("tas-cohort").
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(tasFlavor1.Name).Resource(corev1.ResourceCPU, "4").Obj(),
				*utiltestingapi.MakeFlavorQuotas(tasFlavor2.Name).Resource(corev1.ResourceCPU, "4").Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor2, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	// Only the gate-enabled state is run. This spec cannot discriminate between the two
	// states: a settled ClusterQueue stops advancing AllocatableResourceGeneration, so the
	// recorded flavor progress is never discarded and the Workload escapes the first flavor
	// on its own with the gate off too. What it does cover is the gate-enabled path through
	// the full controller stack rather than the scheduler alone.
	//
	// The gate's own effect needs generation churn on every cycle, which is not something an
	// integration spec can sustain without a background mutator. That is asserted by
	// TestScheduleForPreserveFlavorScanProgress instead, which drives cycles directly and
	// covers both gate states under churn as well as the no-churn outcome.
	ginkgo.It("should admit the workload on the second flavor when the first flavor's topology cannot fit it", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PreserveFlavorScanProgress, true)

		var blocker, pending *kueue.Workload

		ginkgo.By("creating a workload that occupies the whole node of the first flavor", func() {
			blocker = utiltestingapi.MakeWorkload("blocker", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Priority(equalPriority).
				PodSets(*utiltestingapi.MakePodSet("worker", 1).
					NodeSelector(map[string]string{"tas-flavor": "f1"}).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "2").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, blocker)
		})

		ginkgo.By("verifying the blocker is admitted on the first flavor", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, blocker)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(blocker), blocker)).To(gomega.Succeed())
			gomega.Expect(blocker.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
				gomega.Equal(kueue.ResourceFlavorReference(tasFlavor1.Name)))
		})

		ginkgo.By("creating a workload that needs a whole node and so cannot be placed on the first flavor", func() {
			pending = utiltestingapi.MakeWorkload("pending", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Priority(equalPriority).
				PodSets(*utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "2").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, pending)
		})

		ginkgo.By("verifying it is eventually admitted on the second flavor", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pending)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pending), pending)).To(gomega.Succeed())
			gomega.Expect(pending.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
				gomega.Equal(kueue.ResourceFlavorReference(tasFlavor2.Name)))
			ta := utiltas.InternalFrom(pending.Status.Admission.PodSetAssignments[0].TopologyAssignment)
			gomega.Expect(ta.Domains).To(gomega.HaveLen(1))
			gomega.Expect(ta.Domains[0].Values).To(gomega.Equal([]string{"preserve-f2"}))
		})
	})
})
