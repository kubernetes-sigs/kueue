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

package extended

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for DisaggregatedSet", ginkgo.Label("area:tas", "feature:disaggregatedset"), func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
		// decode is a role with a leader and workers, each requesting one
		// extra resource. Every node has one, and a block has four nodes.
		decode *testingds.RoleWrapper
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-ds-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(extraResource, "8").Resource(corev1.ResourceCPU, "2").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("test-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		decode = testingds.MakeRole("decode").
			WithLeader().
			Image(e2e.GetAgnHostImage()).
			Args(e2e.BehaviorWaitForDeletion...).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllDisaggregatedSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	// blocksByPodSet waits until the Pods of the DisaggregatedSet are scheduled
	// and returns the topology blocks of the nodes that run each PodSet.
	blocksByPodSet := func(ds *disaggregatedsetv1.DisaggregatedSet, podCount int) map[string][]string {
		ginkgo.GinkgoHelper()
		pods := &corev1.PodList{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ds.Namespace),
				client.MatchingLabels{disaggregatedsetv1.SetNameLabelKey: ds.Name})).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(podCount))
			for _, pod := range pods.Items {
				g.Expect(pod.Spec.NodeName).NotTo(gomega.BeEmpty())
			}
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())

		blocks := make(map[string][]string)
		for _, pod := range pods.Items {
			node := &corev1.Node{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: pod.Spec.NodeName}, node)).To(gomega.Succeed())
			podSet := pod.Annotations[podconstants.RoleHashAnnotation]
			blocks[podSet] = append(blocks[podSet], node.Labels[utiltesting.DefaultBlockTopologyLevel])
		}
		return blocks
	}

	ginkgo.It("should place the leaders of a role in one block and its workers in one block", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(localQueue.Name).
			Roles(decode.Clone().
				Replicas(2).
				Size(3).
				LeaderTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel)).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)

		blocks := blocksByPodSet(ds, 6)
		gomega.Expect(blocks).To(gomega.HaveKey("decode-leader"))
		gomega.Expect(blocks).To(gomega.HaveKey("decode-worker"))
		gomega.Expect(blocks["decode-leader"]).To(gomega.HaveLen(2))
		gomega.Expect(blocks["decode-leader"]).To(gomega.HaveEach(blocks["decode-leader"][0]))
		gomega.Expect(blocks["decode-worker"]).To(gomega.HaveLen(4))
		gomega.Expect(blocks["decode-worker"]).To(gomega.HaveEach(blocks["decode-worker"][0]))
	})

	ginkgo.It("should place the leader and its workers in one block with a PodSet group", func() {
		ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
			Queue(localQueue.Name).
			Roles(decode.Clone().
				Replicas(1).
				Size(3).
				LeaderTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				WorkerTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				LeaderTemplateAnnotation(kueue.PodSetGroupName, "decode").
				WorkerTemplateAnnotation(kueue.PodSetGroupName, "decode")).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, ds)

		blocks := blocksByPodSet(ds, 3)
		leaderBlock := blocks["decode-leader"]
		gomega.Expect(leaderBlock).To(gomega.HaveLen(1))
		gomega.Expect(blocks["decode-worker"]).To(gomega.HaveLen(2))
		gomega.Expect(blocks["decode-worker"]).To(gomega.HaveEach(leaderBlock[0]))
	})
})
