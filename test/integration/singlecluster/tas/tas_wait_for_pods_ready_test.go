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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Topology Aware Scheduling – WaitForPodsReady with UnhealthyNodes", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		nodes        []corev1.Node
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithConfig(&config.Configuration{
			WaitForPodsReady: &config.WaitForPodsReady{
				Timeout:         metav1.Duration{Duration: 5 * time.Minute},
				RecoveryTimeout: &metav1.Duration{Duration: 1 * time.Second},
				RequeuingStrategy: &config.RequeuingStrategy{
					BackoffLimitCount:  new(int32(100)),
					BackoffBaseSeconds: new(int32(1)),
				},
			},
		}))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-podsready-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

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
			*testingnode.MakeNode("x2").
				Label("node-group", "tas").
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
		}
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
	})

	ginkgo.It("should evict admitted workload with UnhealthyNodes when PodsReady recovery timeout is exceeded", func() {
		var (
			wl *kueue.Workload
		)

		ginkgo.By("creating a workload", func() {
			wl = utiltestingapi.MakeWorkload("wl1", ns.Name).
				PodSets(*utiltestingapi.MakePodSet("worker", 1).
					PreferredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
		})

		ginkgo.By("verify the workload is admitted", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})

		wlKey := client.ObjectKeyFromObject(wl)
		nodeName := nodes[0].Name

		ginkgo.By("making the node NotReady to trigger UnhealthyNodes", func() {
			nodeToUpdate := &corev1.Node{}
			gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

			behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
			})
		})

		ginkgo.By("verify the workload gets UnhealthyNodes", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting PodsReady to False with WaitForRecovery reason in the past", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Second)),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				})
				g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify the workload is evicted due to PodsReady timeout", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadEvicted, kueue.WorkloadEvictedByPodsReadyTimeout))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
