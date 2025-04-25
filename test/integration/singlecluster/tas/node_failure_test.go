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

package core

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("NodeFailure Controller", ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, true)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")
	})

	ginkgo.AfterEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, false)
	})
	ginkgo.When("There is possible replacement node", func() {
		var (
			node1, node2 *corev1.Node
			clusterQueue *kueue.ClusterQueue
			tasCPUFlavor *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			topology     *kueuealpha.Topology
		)
		ginkgo.BeforeEach(func() {
			node1 = testingnode.MakeNode("cpu-node-1").
				Label(corev1.LabelInstanceTypeStable, "cpu-node").
				Label(testing.DefaultRackTopologyLevel, "cpu-rack").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("5"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj()
			node2 = testingnode.MakeNode("cpu-node-2").
				Label(corev1.LabelInstanceTypeStable, "cpu-node").
				Label(testing.DefaultRackTopologyLevel, "cpu-rack").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("5"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj()
			util.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node1, *node2})

			topology = testing.MakeTopology("default").Levels(
				testing.DefaultRackTopologyLevel,
			).Obj()
			util.MustCreate(ctx, k8sClient, topology)

			tasCPUFlavor = testing.MakeResourceFlavor("tas-cpu-flavor").
				NodeLabel(corev1.LabelInstanceTypeStable, "cpu-node").
				TopologyName("default").
				Obj()
			util.MustCreate(ctx, k8sClient, tasCPUFlavor)

			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(tasCPUFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasCPUFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, node1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, node2, true)
		})
		ginkgo.It("should admit a single Pod via TAS", func() {
			p := testingpod.MakePod("test-pod", ns.Name).
				Queue(localQueue.Name).
				Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
				Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				RequestAndLimit("cpu", "200m").
				Obj()

			ginkgo.By("Creating the Pod", func() {
				util.MustCreate(ctx, k8sClient, p)
				gomega.Expect(p.Spec.SchedulingGates).To(gomega.ContainElements(
					corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName},
					corev1.PodSchedulingGate{Name: kueuealpha.TopologySchedulingGate},
				))
			})

			ginkgo.By("waiting for the Pod to be unsuspended", func() {
				jobSetKey := client.ObjectKeyFromObject(p)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, p)).To(gomega.Succeed())
					g.Expect(p.Spec.NodeSelector).To(gomega.Equal(map[string]string{
						"instance-type":      "on-demand",
						corev1.LabelHostname: "kind-worker",
					}))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: podcontroller.GetWorkloadNameForPod(p.Name, p.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By(fmt.Sprintf("await for admission of workload %q and verify TopologyAssignment", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				gomega.Expect(createdWorkload.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{{
							Count:  1,
							Values: []string{"kind-worker"},
						}},
					},
				))
			})

			ginkgo.By("Simulating node failure", func() {

				ginkgo.By("Simulating node failure")
				setNodeCondition(ctx, k8sClient, node1, corev1.NodeReady, corev1.ConditionFalse)

				ginkgo.By("Checking Workload status for failed node")
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedWl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdWorkload), &updatedWl)).Should(gomega.Succeed())
					g.Expect(updatedWl.Status.FailedNodes).Should(gomega.ConsistOf(node1.Name))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

			})
			ginkgo.By(fmt.Sprintf("verify the workload %q gets finished", wlLookupKey), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
					g.Expect(createdWorkload.Status.Conditions).Should(testing.HaveConditionStatusTrue(kueue.WorkloadFinished))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		/*	ginkgo.It("Should update Workload to use the new node when a node fails", func() {
			var wl1 *kueue.Workload
			ginkgo.By("creating a workload which requires block and can fit", func() {
				wl1 = testing.MakeWorkload("wl1", ns.Name).
					Queue(localQueue.Name).Obj()
				ps1 := *testing.MakePodSet("ps1", 1).NodeSelector(
					map[string]string{corev1.LabelInstanceTypeStable: "cpu-node"},
				).Request(corev1.ResourceCPU, "5").Obj()
				ps1.TopologyRequest = &kueue.PodSetTopologyRequest{
					Required: ptr.To(testing.DefaultRackTopologyLevel),
				}
				wl1.Spec.PodSets = []kueue.PodSet{ps1}
				util.MustCreate(ctx, k8sClient, wl1)
			})

			ginkgo.By("verify the workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
			})

			ginkgo.By("verify admission for the workload1", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
				gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
					&kueue.TopologyAssignment{
						Levels: []string{
							testing.DefaultRackTopologyLevel,
						},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"cpu-rack",
								},
							},
						},
					},
				))
			})
			ginkgo.By("Waiting some time")
			time.Sleep(20 * time.Second)
			ginkgo.By("Simulating node failure", func() {

				ginkgo.By("Simulating node failure")
				setNodeCondition(ctx, k8sClient, node1, corev1.NodeReady, corev1.ConditionFalse)

				ginkgo.By("Checking Workload status for failed node")
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedWl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl)).Should(gomega.Succeed())
					g.Expect(updatedWl.Status.FailedNodes).Should(gomega.ConsistOf(node1.Name))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

			})

		})*/
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
