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
	"fmt"
	"slices"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Topology Aware Scheduling", func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup())
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.Context("TAS features, preemption and provisioning", func() {
			ginkgo.When("ProvisioningRequest is used", func() {
				var (
					nodes             []corev1.Node
					ac                *kueue.AdmissionCheck
					prc               *kueue.ProvisioningRequestConfig
					createdRequest    autoscaling.ProvisioningRequest
					ignoreCqCondition = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "Conditions")
				)

				ginkgo.BeforeEach(func() {
					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					prc = utiltestingapi.MakeProvisioningRequestConfig("prov-config").
						ProvisioningClass("provisioning-class").
						RetryLimit(1).
						BaseBackoff(1).
						PodSetUpdate(kueue.ProvisioningRequestPodSetUpdates{
							NodeSelector: []kueue.ProvisioningRequestPodSetUpdatesNodeSelector{{
								Key:                              "dedicated-selector-key",
								ValueFromProvisioningClassDetail: "dedicated-selector-detail",
							}},
						}).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, prc)

					ac = utiltestingapi.MakeAdmissionCheck("provisioning").
						ControllerName(kueue.ProvisioningRequestControllerName).
						Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, ac)
					behavioral.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

					clusterQueue = utiltestingapi.MakeClusterQueue("cq").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
						).AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueue)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &createdRequest, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("should admit workload when nodes are provisioned; Nodes ready after small delay", framework.SlowSpec, func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("create workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Image("image").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					wlKey := client.ObjectKeyFromObject(wl1)

					ginkgo.By("verify the workload reserves the quota", func() {
						behavioral.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					provReqKey := apitypes.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
					}

					ginkgo.By("await for the ProvisioningRequest to be created", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("provision the node which is not ready yet", func() {
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
								NotReady().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
							apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
								Type:   autoscaling.Provisioned,
								Status: metav1.ConditionTrue,
								Reason: autoscaling.Provisioned,
							})
							g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify there is no TAS assignment for the workload yet", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeNil())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("make the node Ready", func() {
						createdNode := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, createdNode)).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
							createdNode.Status.Conditions = []corev1.NodeCondition{{
								Type:   corev1.NodeReady,
								Status: corev1.ConditionTrue,
							}}
							g.Expect(k8sClient.Status().Update(ctx, createdNode)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
							createdNode.Spec.Taints = slices.DeleteFunc(createdNode.Spec.Taints, func(taint corev1.Taint) bool {
								return taint.Key == corev1.TaintNodeNotReady
							})
							g.Expect(k8sClient.Update(ctx, createdNode)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify TAS admission for the workload", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{
										corev1.LabelHostname,
									},
									Domains: []utiltas.TopologyDomainAssignment{
										{
											Count: 1,
											Values: []string{
												"x1",
											},
										},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})
				})

				ginkgo.It("should admit workload when nodes are provisioned; limits-only requested by Workload; with restart", framework.SlowSpec, func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("create workload with limits-only", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
								Limit(corev1.ResourceCPU, "1").
								PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Image("image").
								Obj(),
							).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					wlKey := client.ObjectKeyFromObject(wl1)

					ginkgo.By("verify the workload reserves the quota", func() {
						behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
					})

					provReqKey := apitypes.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
					}

					ginkgo.By("await for the ProvisioningRequest to be created", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("provision the nodes but NotReady to keep the workload inadmissible by TAS", func() {
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
								NotReady().
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
								NotReady().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
							apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
								Type:   autoscaling.Provisioned,
								Status: metav1.ConditionTrue,
								Reason: autoscaling.Provisioned,
							})
							g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("await for the admission check to be ready", func() {
						behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStateReady)
					})

					ginkgo.By("restart Kueue manager", func() {
						fwk.StopManager(ctx)
						fwk.StartManager(ctx, cfg, managerSetup())
					})

					for _, node := range nodes {
						ginkgo.By(fmt.Sprintf("make the node %s Ready to make the workload admissible", node.Name), func() {
							createdNode := &corev1.Node{}
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: node.Name}, createdNode)).Should(gomega.Succeed())

							gomega.Eventually(func(g gomega.Gomega) {
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
								createdNode.Status.Conditions = []corev1.NodeCondition{{
									Type:   corev1.NodeReady,
									Status: corev1.ConditionTrue,
								}}
								g.Expect(k8sClient.Status().Update(ctx, createdNode)).Should(gomega.Succeed())
							}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

							gomega.Eventually(func(g gomega.Gomega) {
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
								createdNode.Spec.Taints = slices.DeleteFunc(createdNode.Spec.Taints, func(taint corev1.Taint) bool {
									return taint.Key == corev1.TaintNodeNotReady
								})
								g.Expect(k8sClient.Update(ctx, createdNode)).Should(gomega.Succeed())
							}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
						})
					}

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})
				})

				ginkgo.It("should admit workload when nodes are provisioned; only-limits requested by Workload", framework.SlowSpec, func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("create workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
								Limit(corev1.ResourceCPU, "1").
								PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Image("image").
								Obj(),
							).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					wlKey := client.ObjectKeyFromObject(wl1)

					ginkgo.By("verify the workload reserves the quota", func() {
						behavioral.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					provReqKey := apitypes.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
					}

					ginkgo.By("await for the ProvisioningRequest to be created", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("provision the nodes", func() {
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

					ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
							apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
								Type:   autoscaling.Provisioned,
								Status: metav1.ConditionTrue,
								Reason: autoscaling.Provisioned,
							})
							g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})
				})

				ginkgo.It("should admit workload targeting the dedicated newly provisioned nodes", framework.SlowSpec, func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("create workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Image("image").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					wlKey := client.ObjectKeyFromObject(wl1)

					ginkgo.By("verify the workload reserves the quota", func() {
						behavioral.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					provReqKey := apitypes.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
					}

					ginkgo.By("await for the ProvisioningRequest to be created", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("provision the nodes, only one of them matches the ProvisiningRequest", func() {
						//      b1
						//   /      \
						//  r1       r2
						//  |         |
						//  x2       x1
						nodes = []corev1.Node{
							*testingnode.MakeNode("x2").
								Label("node-group", "tas").
								Label("dedicated-selector-key", "dedicated-selector-value-abc").
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
							*testingnode.MakeNode("x1").
								Label("node-group", "tas").
								Label("dedicated-selector-key", "dedicated-selector-value-xyz").
								Label(utiltesting.DefaultBlockTopologyLevel, "b1").
								Label(utiltesting.DefaultRackTopologyLevel, "r2").
								Label(corev1.LabelHostname, "x1").
								StatusAllocatable(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("2Gi"),
									corev1.ResourcePods:   resource.MustParse("10"),
								}).
								Ready().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
							apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
								Type:   autoscaling.Provisioned,
								Status: metav1.ConditionTrue,
								Reason: autoscaling.Provisioned,
							})
							createdRequest.Status.ProvisioningClassDetails = make(map[string]autoscaling.Detail)
							createdRequest.Status.ProvisioningClassDetails["dedicated-selector-detail"] = "dedicated-selector-value-xyz"
							g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify admission for the workload", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{
										corev1.LabelHostname,
									},
									Domains: []utiltas.TopologyDomainAssignment{
										{
											Count: 1,
											Values: []string{
												"x1",
											},
										},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})
				})

				ginkgo.It("should admit workload when nodes are provisioned and account for quota usage correctly", framework.SlowSpec, func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Image("image").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					wlKey := client.ObjectKeyFromObject(wl1)

					ginkgo.By("verify the workload reserves the quota", func() {
						behavioral.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("check queue resource consumption before full admission", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
							g.Expect(clusterQueue.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
								PendingWorkloads:   0,
								ReservingWorkloads: 1,
								AdmittedWorkloads:  0,
								FlavorsReservation: []kueue.FlavorUsage{{
									Name: kueue.ResourceFlavorReference(tasFlavor.Name),
									Resources: []kueue.ResourceUsage{{
										Name:  corev1.ResourceCPU,
										Total: resource.MustParse("1"),
									}},
								}},
								FlavorsUsage: []kueue.FlavorUsage{{
									Name: kueue.ResourceFlavorReference(tasFlavor.Name),
									Resources: []kueue.ResourceUsage{{
										Name: corev1.ResourceCPU,
									}},
								}},
							}, ignoreCqCondition))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					provReqKey := apitypes.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
					}

					ginkgo.By("await for the ProvisioningRequest to be created", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("provision the nodes which are ready", func() {
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
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
							apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
								Type:   autoscaling.Provisioned,
								Status: metav1.ConditionTrue,
								Reason: autoscaling.Provisioned,
							})
							g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify admission for the workload contains the TopologyAssignment", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{
										corev1.LabelHostname,
									},
									Domains: []utiltas.TopologyDomainAssignment{
										{
											Count: 1,
											Values: []string{
												"x1",
											},
										},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("Verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})

					ginkgo.By("Check queue resource consumption after full admission", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
							g.Expect(clusterQueue.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
								PendingWorkloads:   0,
								ReservingWorkloads: 1,
								AdmittedWorkloads:  1,
								FlavorsReservation: []kueue.FlavorUsage{{
									Name: kueue.ResourceFlavorReference(tasFlavor.Name),
									Resources: []kueue.ResourceUsage{{
										Name:  corev1.ResourceCPU,
										Total: resource.MustParse("1"),
									}},
								}},
								FlavorsUsage: []kueue.FlavorUsage{{
									Name: kueue.ResourceFlavorReference(tasFlavor.Name),
									Resources: []kueue.ResourceUsage{{
										Name:  corev1.ResourceCPU,
										Total: resource.MustParse("1"),
									}},
								}},
							}, ignoreCqCondition))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should admit workload when nodes are provisioned; manager restart", framework.SlowSpec, func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("create workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Image("image").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					wlKey := client.ObjectKeyFromObject(wl1)

					ginkgo.By("verify the workload reserves the quota", func() {
						behavioral.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					})

					provReqKey := apitypes.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
					}

					ginkgo.By("await for the ProvisioningRequest to be created", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("provision the nodes", func() {
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
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
							apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
								Type:   autoscaling.Provisioned,
								Status: metav1.ConditionTrue,
								Reason: autoscaling.Provisioned,
							})
							g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("await for the check to be ready", func() {
						behavioral.ExpectAdmissionCheckState(ctx, k8sClient, wlKey, ac.Name, kueue.CheckStateReady)
					})

					ginkgo.By("restart Kueue manager", func() {
						fwk.StopManager(ctx)
						fwk.StartManager(ctx, cfg, managerSetup())
					})

					ginkgo.By("verify admission for the workload", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{
										corev1.LabelHostname,
									},
									Domains: []utiltas.TopologyDomainAssignment{
										{
											Count: 1,
											Values: []string{
												"x1",
											},
										},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})
				})

				ginkgo.It("uses exponential second-pass backoff for the workload admission", framework.SlowSpec, func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("create workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").
							PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
								Request(corev1.ResourceCPU, "1").
								PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Image("image").
								Obj(),
							).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					wlKey := client.ObjectKeyFromObject(wl1)

					ginkgo.By("verify the workload reserves the quota and requires TAS assignment (second pass)", func() {
						behavioral.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					provReqKey := apitypes.NamespacedName{
						Namespace: wlKey.Namespace,
						Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
					}

					ginkgo.By("await ProvisioningRequest creation", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("provision a node that is NOT Ready", func() {
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
								NotReady().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("mark ProvisioningRequest as Provisioned (start of the backoff window)", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
							apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
								Type:   autoscaling.Provisioned,
								Status: metav1.ConditionTrue,
								Reason: autoscaling.Provisioned,
							})
							g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("ensure no assignment during three backoff intervals (≈1s, 2s, 4s - total 7s)", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeNil())
						}, 7*time.Second, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("observe at least three SecondPassFailed events while the node is NotReady (≈1s, 2s, 4s backoffs)", func() {
						var evList eventsv1.EventList
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.List(ctx, &evList, &client.ListOptions{Namespace: ns.Name})).To(gomega.Succeed())
							var count int32
							for i := range evList.Items {
								ev := evList.Items[i]
								if ev.Reason == "SecondPassFailed" && ev.Regarding.Name == wl1.Name {
									if ev.Series != nil {
										count += ev.Series.Count + 1
									} else {
										count++
									}
								}
							}
							// During second-pass scheduling, each status update (such as QuotaReserved=False)
							// triggers WorkloadReconciler.Update, which calls QueueSecondPassIfNeeded with
							// iteration=0. This registers an additional backoff timer while the workload is
							// already waiting in secondPassQueue, causing more than one wakeup and SecondPassFailed
							// event per backoff interval. We verify that at least three attempts occurred.
							g.Expect(count).Should(gomega.BeNumerically(">=", int32(3)))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("make the node Ready", func() {
						createdNode := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, createdNode)).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
							createdNode.Status.Conditions = []corev1.NodeCondition{{
								Type:   corev1.NodeReady,
								Status: corev1.ConditionTrue,
							}}
							g.Expect(k8sClient.Status().Update(ctx, createdNode)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
							createdNode.Spec.Taints = slices.DeleteFunc(createdNode.Spec.Taints, func(taint corev1.Taint) bool {
								return taint.Key == corev1.TaintNodeNotReady
							})
							g.Expect(k8sClient.Update(ctx, createdNode)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("admit after nodes become Ready (after 8s backoff finishes)", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{{
										Count:  1,
										Values: []string{"x1"},
									}},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})
				})
			})
		})
	})
})
