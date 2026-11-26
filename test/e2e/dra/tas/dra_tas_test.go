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
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// The driver publishes two GPUs on each of the two worker nodes, so each node takes two
// Pods of one GPU. The quota allows more GPUs than the nodes have, so that only the
// device check limits where Pods go.
var _ = ginkgo.Describe("DRA device feasibility with TAS", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		lowPriority  *kueue.WorkloadPriorityClass
		highPriority *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-dra-tas-")

		topology = utiltestingapi.MakeDefaultOneLevelTopology("hostname-" + ns.Name)
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-gpu-"+ns.Name).
			NodeLabel(corev1.LabelOSStable, "linux").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("tas-gpu-cq-" + ns.Name).
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "4").
				Resource("gpu", "8").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("tas-gpu-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		lowPriority = utiltestingapi.MakeWorkloadPriorityClass("low-" + ns.Name).PriorityValue(10).Obj()
		behavioral.MustCreate(ctx, k8sClient, lowPriority)
		highPriority = utiltestingapi.MakeWorkloadPriorityClass("high-" + ns.Name).PriorityValue(100).Obj()
		behavioral.MustCreate(ctx, k8sClient, highPriority)

		rct := utiltesting.MakeResourceClaimTemplate("one-gpu", ns.Name).
			DeviceRequest("gpu", e2e.DRAExampleDriverName, 1).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, rct)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriority, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, highPriority, true)
	})

	// TAS packs Pods onto as few nodes as their CPU allows. Without counting the devices,
	// all four would go to one node, and two would stay Pending.
	ginkgo.It("should spread Pods by the devices each node has", func() {
		job := testingjob.MakeJob("spread", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(4).
			Completions(4).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			ResourceClaimTemplate("gpu", "one-gpu").
			PodAnnotation(kueue.PodSetPreferredTopologyAnnotation, corev1.LabelHostname).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job)

		ginkgo.By("running two Pods on each node", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(runningPodsByNode(g, ns, job)).To(gomega.ConsistOf(int32(2), int32(2)))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	// The second Job is admitted before kube-scheduler binds the first one's Pods, so
	// their claims are not allocated yet. Kueue still counts the devices it promised.
	ginkgo.It("should give two Jobs admitted one after the other a node each", func() {
		first := testingjob.MakeJob("first", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			ResourceClaimTemplate("gpu", "one-gpu").
			PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, first)
		second := testingjob.MakeJob("second", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			ResourceClaimTemplate("gpu", "one-gpu").
			PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, second)

		ginkgo.By("running both Jobs on different nodes", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				firstNodes := runningPodsByNode(g, ns, first)
				secondNodes := runningPodsByNode(g, ns, second)
				g.Expect(firstNodes).To(gomega.ConsistOf(int32(2)))
				g.Expect(secondNodes).To(gomega.ConsistOf(int32(2)))
				for node := range firstNodes {
					g.Expect(secondNodes).NotTo(gomega.HaveKey(node))
				}
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	// Two victims would also free the devices, but one is enough.
	ginkgo.It("should preempt one Workload for the devices, not two", func() {
		low1 := testingjob.MakeJob("low-1", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(lowPriority.Name).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			ResourceClaimTemplate("gpu", "one-gpu").
			PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, low1)
		low2 := testingjob.MakeJob("low-2", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(lowPriority.Name).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			ResourceClaimTemplate("gpu", "one-gpu").
			PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, low2)
		ginkgo.By("waiting for the low-priority Jobs to hold every device", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(runningPodsByNode(g, ns, low1)).To(gomega.ConsistOf(int32(2)))
				g.Expect(runningPodsByNode(g, ns, low2)).To(gomega.ConsistOf(int32(2)))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		high := testingjob.MakeJob("high", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(highPriority.Name).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(corev1.ResourceCPU, "100m").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			ResourceClaimTemplate("gpu", "one-gpu").
			PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, high)

		ginkgo.By("running the high-priority Job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(runningPodsByNode(g, ns, high)).To(gomega.ConsistOf(int32(2)))
			}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("preempting only one of the low-priority Workloads", func() {
			preempted := 0
			for _, job := range []*batchv1.Job{low1, low2} {
				wl := &kueue.Workload{}
				key := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
				gomega.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
				// The Preempted condition is cleared when the victim is admitted again, the eviction count is not.
				if wl.Status.SchedulingStats != nil && slices.ContainsFunc(wl.Status.SchedulingStats.Evictions, func(e kueue.WorkloadSchedulingStatsEviction) bool {
					return e.Reason == kueue.WorkloadEvictedByPreemption
				}) {
					preempted++
				}
			}
			gomega.Expect(preempted).To(gomega.Equal(1))
		})
	})
})

// runningPodsByNode counts the Job's running Pods on each node.
func runningPodsByNode(g gomega.Gomega, ns *corev1.Namespace, job *batchv1.Job) map[string]int32 {
	var pods corev1.PodList
	g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns.Name), client.MatchingLabels{batchv1.JobNameLabel: job.Name})).To(gomega.Succeed())
	byNode := make(map[string]int32)
	for _, pod := range pods.Items {
		if pod.Status.Phase == corev1.PodRunning {
			byNode[pod.Spec.NodeName]++
		}
	}
	return byNode
}
