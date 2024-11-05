/*
Copyright 2024 The Kubernetes Authors.

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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	tasBlockLabel = "cloud.com/topology-block"
	tasRackLabel  = "cloud.com/topology-rack"
)

var _ = ginkgo.Describe("Topology Aware Scheduling", ginkgo.Ordered, func() {
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
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "tas-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, false)
	})

	ginkgo.When("Negative scenarios for ResourceFlavor configuration", func() {
		ginkgo.It("should not allow to create ResourceFlavor with invalid topology name", func() {
			tasFlavor := testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("invalid topology name").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})

		ginkgo.It("should not allow to create ResourceFlavor without any node labels", func() {
			tasFlavor := testing.MakeResourceFlavor("tas-flavor").TopologyName("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})
	})

	ginkgo.When("Negative scenarios for ClusterQueue configuration", func() {
		var (
			topology       *kueuealpha.Topology
			tasFlavor      *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			admissionCheck *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			topology = testing.MakeTopology("default").Levels([]string{
				tasBlockLabel,
				tasRackLabel,
			}).Obj()
			gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

			tasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
		})

		ginkgo.It("should mark TAS ClusterQueue as inactive if used in cohort", func() {
			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).Cohort("cohort").Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

			gomega.Eventually(func() []metav1.Condition {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
				{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "NotSupportedWithTopologyAwareScheduling",
					Message: `Can't admit new workloads: TAS is not supported for cohorts.`,
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		})

		ginkgo.It("should mark TAS ClusterQueue as inactive if used with preemption", func() {
			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

			gomega.Eventually(func() []metav1.Condition {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
				{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "NotSupportedWithTopologyAwareScheduling",
					Message: `Can't admit new workloads: TAS is not supported for preemption within cluster queue.`,
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		})

		ginkgo.It("should mark TAS ClusterQueue as inactive if used with MultiKueue", func() {
			admissionCheck = testing.MakeAdmissionCheck("multikueue").ControllerName(kueue.MultiKueueControllerName).Obj()
			gomega.Expect(k8sClient.Create(ctx, admissionCheck)).To(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)

			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks(admissionCheck.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

			gomega.Eventually(func() []metav1.Condition {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
				{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "NotSupportedWithTopologyAwareScheduling",
					Message: `Can't admit new workloads: TAS is not supported with MultiKueue admission check.`,
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		})

		ginkgo.It("should mark TAS ClusterQueue as inactive if used with ProvisioningRequest", func() {
			admissionCheck = testing.MakeAdmissionCheck("provisioning").ControllerName(kueue.ProvisioningRequestControllerName).Obj()
			gomega.Expect(k8sClient.Create(ctx, admissionCheck)).To(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)

			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks(admissionCheck.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

			gomega.Eventually(func() []metav1.Condition {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
				{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "NotSupportedWithTopologyAwareScheduling",
					Message: `Can't admit new workloads: TAS is not supported with ProvisioningRequest admission check.`,
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		})
	})

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueuealpha.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.When("Nodes are created before test", func() {
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "b1-r1",
							Labels: map[string]string{
								"node-group":  "tas",
								tasBlockLabel: "b1",
								tasRackLabel:  "r1",
							},
						},
						Status: corev1.NodeStatus{
							Allocatable: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Conditions: []corev1.NodeCondition{
								{
									Type:   corev1.NodeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "b1-r2",
							Labels: map[string]string{
								"node-group":  "tas",
								tasBlockLabel: "b1",
								tasRackLabel:  "r2",
							},
						},
						Status: corev1.NodeStatus{
							Allocatable: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Conditions: []corev1.NodeCondition{
								{
									Type:   corev1.NodeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "b2-r1",
							Labels: map[string]string{
								"node-group":  "tas",
								tasBlockLabel: "b2",
								tasRackLabel:  "r1",
							},
						},
						Status: corev1.NodeStatus{
							Allocatable: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Conditions: []corev1.NodeCondition{
								{
									Type:   corev1.NodeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "b2-r2",
							Labels: map[string]string{
								"node-group":  "tas",
								tasBlockLabel: "b2",
								tasRackLabel:  "r2",
							},
						},
						Status: corev1.NodeStatus{
							Allocatable: corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
							},
							Conditions: []corev1.NodeCondition{
								{
									Type:   corev1.NodeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				}
				for _, node := range nodes {
					gomega.Expect(k8sClient.Create(ctx, &node)).Should(gomega.Succeed())
					gomega.Expect(k8sClient.Status().Update(ctx, &node)).Should(gomega.Succeed())
				}

				topology = testing.MakeTopology("default").Levels([]string{
					tasBlockLabel,
					tasRackLabel,
				}).Obj()
				gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should not admit workload which does not fit to the required topology domain", func() {
				ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
					wl1 := testing.MakeWorkload("wl1-inadmissible", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					}
					gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})
			})

			ginkgo.It("should admit workload which fits in a required topology domain", func() {
				var wl1, wl2, wl3, wl4 *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].Count = 2
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					}
					gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
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
								tasBlockLabel,
								tasRackLabel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"b1",
										"r1",
									},
								},
								{
									Count: 1,
									Values: []string{
										"b1",
										"r2",
									},
								},
							},
						},
					))
				})

				ginkgo.By("creating the second workload to see it lands on another rack", func() {
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl2.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					}
					gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
				})

				ginkgo.By("verify admission for the workload2", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
					gomega.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								tasBlockLabel,
								tasRackLabel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"b2",
										"r1",
									},
								},
							},
						},
					))
				})

				ginkgo.By("creating the wl3", func() {
					wl3 = testing.MakeWorkload("wl3", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl3.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					}
					gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the wl3 is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2, wl3)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 3)
				})

				ginkgo.By("verify admission for the wl3", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), wl3)).To(gomega.Succeed())
					gomega.Expect(wl3.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								tasBlockLabel,
								tasRackLabel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"b2",
										"r2",
									},
								},
							},
						},
					))
				})

				ginkgo.By("creating wl4", func() {
					wl4 = testing.MakeWorkload("wl4", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl4.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					}
					gomega.Expect(k8sClient.Create(ctx, wl4)).Should(gomega.Succeed())
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl4)
				})

				ginkgo.By("finish wl3", func() {
					util.FinishWorkloads(ctx, k8sClient, wl3)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
				})

				ginkgo.By("verify the wl4 is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2, wl4)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 3)
				})

				ginkgo.By("verify admission for the wl4", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl4), wl4)).To(gomega.Succeed())
					gomega.Expect(wl4.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								tasBlockLabel,
								tasRackLabel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"b2",
										"r2",
									},
								},
							},
						},
					))
				})
			})
		})

		ginkgo.When("Node structure is mutated during test cases", func() {
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				ns = &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						GenerateName: "tas-",
					},
				}
				gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

				topology = testing.MakeTopology("default").Levels([]string{
					tasBlockLabel,
					tasRackLabel,
				}).Obj()
				gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.Succeed())

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should admit workload when nodes become available", func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					}
					gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("Create nodes to allow scheduling", func() {
					nodes = []corev1.Node{
						{
							ObjectMeta: metav1.ObjectMeta{
								Name: "b1-r1",
								Labels: map[string]string{
									"node-group":  "tas",
									tasBlockLabel: "b1",
									tasRackLabel:  "r1",
								},
							},
							Status: corev1.NodeStatus{
								Allocatable: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
								},
								Conditions: []corev1.NodeCondition{
									{
										Type:   corev1.NodeReady,
										Status: corev1.ConditionTrue,
									},
								},
							},
						},
					}
					for _, node := range nodes {
						gomega.Expect(k8sClient.Create(ctx, &node)).Should(gomega.Succeed())
						gomega.Expect(k8sClient.Status().Update(ctx, &node)).Should(gomega.Succeed())
					}
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})
		})
	})

	ginkgo.When("Multiple TAS Resource Flavors in queue", func() {
		var (
			tasGPUFlavor *kueue.ResourceFlavor
			tasCPUFlavor *kueue.ResourceFlavor
			topology     *kueuealpha.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.When("Nodes are created before test", func() {
			const (
				gpuResName = "example.com/gpu"
			)
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "cpu-node",
							Labels: map[string]string{
								"node.kubernetes.io/instance-type": "cpu-node",
								tasRackLabel:                       "cpu-rack",
							},
						},
						Status: corev1.NodeStatus{
							Allocatable: corev1.ResourceList{
								corev1.ResourceCPU: resource.MustParse("5"),
							},
							Conditions: []corev1.NodeCondition{
								{
									Type:   corev1.NodeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{
							Name: "gpu-node",
							Labels: map[string]string{
								"node.kubernetes.io/instance-type": "gpu-node",
								tasRackLabel:                       "gpu-rack",
							},
						},
						Status: corev1.NodeStatus{
							Allocatable: corev1.ResourceList{
								gpuResName: resource.MustParse("4"),
							},
							Conditions: []corev1.NodeCondition{
								{
									Type:   corev1.NodeReady,
									Status: corev1.ConditionTrue,
								},
							},
						},
					},
				}
				for _, node := range nodes {
					gomega.Expect(k8sClient.Create(ctx, &node)).Should(gomega.Succeed())
					gomega.Expect(k8sClient.Status().Update(ctx, &node)).Should(gomega.Succeed())
				}

				topology = testing.MakeTopology("default").Levels([]string{
					tasRackLabel,
				}).Obj()
				gomega.Expect(k8sClient.Create(ctx, topology)).Should(gomega.Succeed())

				tasGPUFlavor = testing.MakeResourceFlavor("tas-gpu-flavor").
					NodeLabel("node.kubernetes.io/instance-type", "gpu-node").
					TopologyName("default").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, tasGPUFlavor)).Should(gomega.Succeed())

				tasCPUFlavor = testing.MakeResourceFlavor("tas-cpu-flavor").
					NodeLabel("node.kubernetes.io/instance-type", "cpu-node").
					TopologyName("default").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, tasCPUFlavor)).Should(gomega.Succeed())

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas(tasGPUFlavor.Name).Resource(corev1.ResourceCPU, "1").Resource(gpuResName, "5").Obj(),
						*testing.MakeFlavorQuotas(tasCPUFlavor.Name).Resource(corev1.ResourceCPU, "5").Resource(gpuResName, "0").Obj(),
					).Obj()
				gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasCPUFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasGPUFlavor, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should admit workload which fits in a required topology domain", func() {
				var wl1 *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(localQueue.Name).Obj()
					ps1 := *testing.MakePodSet("manager", 1).NodeSelector(
						map[string]string{"node.kubernetes.io/instance-type": "cpu-node"},
					).Request(corev1.ResourceCPU, "5").Obj()
					ps1.TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					}
					ps2 := *testing.MakePodSet("worker", 2).NodeSelector(
						map[string]string{"node.kubernetes.io/instance-type": "gpu-node"},
					).Request(gpuResName, "2").Obj()
					ps2.TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					}
					wl1.Spec.PodSets = []kueue.PodSet{ps1, ps2}
					gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
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
								tasRackLabel,
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
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								tasRackLabel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 2,
									Values: []string{
										"gpu-rack",
									},
								},
							},
						},
					))
				})
			})
		})
	})
})
