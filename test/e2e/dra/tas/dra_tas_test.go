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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "4").
				Resource("gpu", "8").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("tas-gpu-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

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
