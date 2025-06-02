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
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/tas"
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
			numPods      int
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

			numPods = 4
			sampleJob := testingjob.MakeJob("ranks-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
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
					g.Expect(sampleJob.Status.Ready).Should(gomega.Equal(ptr.To(int32(numPods))))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
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

		ginkgo.It("Should update nodesToReplace at the workload when a node fails", func() {
			pods := &corev1.PodList{}
			ginkgo.By("Ensure all pods are running on nodes", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			node := &corev1.Node{}
			chosenPod := pods.Items[0]
			var wlName string
			ginkgo.By("Get node running the chosen pod", func() {
				var found bool
				wlName, found = chosenPod.Annotations[kueuealpha.WorkloadAnnotation]
				gomega.Expect(found).Should(gomega.BeTrue())
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: chosenPod.Spec.NodeName}, node)).To(gomega.Succeed())
			})

			ginkgo.DeferCleanup(func() {
				ginkgo.By(fmt.Sprintf("Restoring original Ready status of node %s", node.Name))
				util.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				})
			})

			ginkgo.By(fmt.Sprintf("Simulate failure of node %s hosting pod %s", node.Name, chosenPod.Name), func() {
				util.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
				})
			})
			ginkgo.By(fmt.Sprintf("Verify node is added to failure list by checking workload %s status", wlName), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: wlName, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					g.Expect(wl.GetAnnotations()[kueuealpha.NodeToReplaceAnnotation]).To(gomega.Equal(chosenPod.Spec.NodeName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should update nodesToReplace at the workload when a node is deleted", func() {
			pods := &corev1.PodList{}
			ginkgo.By("Ensure all pods are running on nodes", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			chosenPod := pods.Items[0]
			nodeNameToDelete := chosenPod.Spec.NodeName

			var wlName string
			var found bool
			wlName, found = chosenPod.Annotations[kueuealpha.WorkloadAnnotation]
			gomega.Expect(found).Should(gomega.BeTrue(), "Workload annotation should be present on the pod")

			// Store the node object to re-create it later
			originalNode := &corev1.Node{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeNameToDelete}, originalNode)).To(gomega.Succeed())

			ginkgo.DeferCleanup(func() {
				ginkgo.By(fmt.Sprintf("Re-creating node %s", nodeNameToDelete))
				originalNode.ResourceVersion = ""
				originalNode.UID = ""
				originalNode.ManagedFields = nil
				util.MustCreate(ctx, k8sClient, originalNode)

				util.SetNodeCondition(ctx, k8sClient, originalNode, &corev1.NodeCondition{
					Type:   corev1.NodeReady,
					Status: corev1.ConditionTrue,
				})
			})

			ginkgo.By(fmt.Sprintf("Simulate deletion of node %s hosting pod %s", nodeNameToDelete, chosenPod.Name), func() {
				gomega.Expect(k8sClient.Delete(ctx, originalNode)).To(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Verify node %s is added to failure list by checking workload %s status", nodeNameToDelete, wlName), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: wlName, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					g.Expect(wl.GetAnnotations()[kueuealpha.NodeToReplaceAnnotation]).To(gomega.Equal(nodeNameToDelete))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
