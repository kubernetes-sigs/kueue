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

package tase2e

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("NodeFailure Controller", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-job-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Job", func() {
		var (
			topology     *kueuealpha.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeDefaultThreeLevelTopology("datacenter")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).TopologyName(topology.Name).Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			numPods := 4
			sampleJob := testingjob.MakeJob("ranks-job", ns.Name).
				Queue(localQueue.Name).
				Parallelism(int32(numPods)).
				Completions(int32(numPods)).
				Indexed(true).
				RequestAndLimit(extraResource, "1").
				Obj()
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, testing.DefaultBlockTopologyLevel).
				Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
				Obj()
			util.MustCreate(ctx, k8sClient, sampleJob)

			ginkgo.By("Job is unsuspended, and has all Pods active and ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Active).Should(gomega.Equal(int32(numPods)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sampleJob), sampleJob)).To(gomega.Succeed())
					g.Expect(sampleJob.Status.Ready).Should(gomega.Equal(ptr.To[int32](int32(numPods))))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			pods := &corev1.PodList{}
			ginkgo.By("Ensure all pods are running on nodes", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
			node := &corev1.Node{}
			chosenPod := pods.Items[3]
			var wlName string
			ginkgo.By("Get node running the chosen pod", func() {
				var found bool
				wlName, found = chosenPod.Annotations[kueuealpha.WorkloadAnnotation]
				gomega.Expect(found).Should(gomega.BeTrue())
				nodeList := &corev1.NodeList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, nodeList)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				for _, n := range nodeList.Items {
					if n.Name == chosenPod.Spec.NodeName {
						node = &n
						break
					}
				}
			})
			ginkgo.By(fmt.Sprintf("Crashing node %s hosting pod %s", node.Name, chosenPod.Name), func() {
				setNodeCondition(ctx, k8sClient, node, corev1.NodeReady, corev1.ConditionFalse)
			})

			// Add a short wait to allow the controller to react
			ginkgo.By("Waiting for controller to process node failure", func() {
				time.Sleep(2 * time.Second)
			})
			ginkgo.By(fmt.Sprintf("Verify node is added to failure list by checking workload %s status", wlName), func() {
				wl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					k8sClient.Get(ctx, client.ObjectKey{Name: wlName, Namespace: ns.Name}, wl)
					gomega.Expect(wl.Status.FailedNodes).Should(gomega.HaveLen(1))
					gomega.Expect(wl.Status.FailedNodes[0]).Should(gomega.Equal(chosenPod.Spec.NodeName))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Recover node %s hosting pod %s", node.Name, chosenPod.Name), func() {
				setNodeCondition(ctx, k8sClient, node, corev1.NodeReady, corev1.ConditionTrue)
			})
			ginkgo.By("Waiting for controller to process node recovery", func() {
				time.Sleep(2 * time.Second)
			})
			ginkgo.By(fmt.Sprintf("Verify node is removed from failure list by checking workload %s status", wlName), func() {
				wl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					k8sClient.Get(ctx, client.ObjectKey{Name: wlName, Namespace: ns.Name}, wl)
					gomega.Expect(wl.Status.FailedNodes).Should(gomega.HaveLen(0))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

func setNodeCondition(ctx context.Context, k8sClient client.Client, node *corev1.Node, conditionType corev1.NodeConditionType, conditionStatus corev1.ConditionStatus) {
	gomega.Eventually(func(g gomega.Gomega) {
		var updatedNode corev1.Node
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), &updatedNode)).To(gomega.Succeed())
		for i := range updatedNode.Status.Conditions {
			if updatedNode.Status.Conditions[i].Type == conditionType {
				updatedNode.Status.Conditions[i].Status = conditionStatus
			}
		}
		g.Expect(k8sClient.Status().Update(ctx, &updatedNode)).To(gomega.Succeed())
	}, util.Timeout, util.Interval).Should(gomega.Succeed(), "Failed to set node condition %s to %s for node %s", conditionType, conditionStatus, node.Name)
}
