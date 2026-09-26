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

package baseline

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	deploymenttesting "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for Deployment", ginkgo.Label(util.Shard1, "area:tas", "feature:deployment"), func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-deployment-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(corev1.ResourceCPU, "2").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("test-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("creating a Deployment with topology spreading across replicas", func() {
		ginkgo.It("should spread the replicas evenly across blocks", func() {
			const replicas = int32(4)

			// Each replica of a Deployment is admitted as its own single-Pod
			// Workload, and every one of those Workloads is labelled with the
			// Deployment's UID (kueue.x-k8s.io/job-uid). The annotation below
			// therefore omits workloadLabelSelectors: they default to the job
			// UID, so the replicas are spread against each other and nothing
			// else. With a 0.5 per-block allowance, a block that already holds
			// more than half of the admitted replicas is banned for the next
			// one, so 4 replicas over the 2 blocks must end up 2 and 2. The
			// pods are small enough (10m CPU) that all 4 would otherwise fit on
			// a single node, so the split can only come from the spreading rule.
			spreadingAnnotation := fmt.Sprintf(
				`{"rules":[{"topologyKey":%q,"maxShareAllowingPlacement":"0.5","enforcementMode":"Required"}]}`,
				utiltesting.DefaultBlockTopologyLevel,
			)

			deployment := deploymenttesting.MakeDeployment("deployment", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "10m").
				Replicas(replicas).
				Queue(localQueue.Name).
				PodTemplateAnnotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				PodTemplateAnnotation(kueue.PodSetTopologySpreadingAnnotation, spreadingAnnotation).
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating a Deployment", func() {
				util.MustCreate(ctx, k8sClient, deployment)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
					g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(replicas))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("Ensuring all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(int(replicas)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying every replica's Workload is labelled with the Deployment UID", func() {
				for _, p := range pods.Items {
					wl := &kueue.Workload{}
					wlKey := types.NamespacedName{Name: pod.GetWorkloadNameForPod(p.Name, p.UID), Namespace: p.Namespace}
					gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					gomega.Expect(wl.Labels).To(gomega.HaveKeyWithValue(controllerconstants.JobUIDLabel, string(deployment.UID)))
				}
			})

			ginkgo.By("Verifying no block holds more than 2 of the 4 replicas", func() {
				podsPerBlock := make(map[string]int, 2)
				for _, p := range pods.Items {
					block, found := blockOfNode[p.Spec.NodeName]
					gomega.Expect(found).To(gomega.BeTrue(), "pod %s landed on unexpected node %s", p.Name, p.Spec.NodeName)
					podsPerBlock[block]++
				}
				for block, count := range podsPerBlock {
					gomega.Expect(count).To(gomega.BeNumerically("<=", 2),
						"block %s holds %d of the %d replicas, exceeding the 50%% spreading allowance", block, count, replicas)
				}
			})
		})
	})
})
