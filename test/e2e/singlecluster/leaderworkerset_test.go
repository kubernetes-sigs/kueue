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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("LeaderWorkerSet integration", func() {
	const (
		resourceFlavorName = "lws-rf"
		clusterQueueName   = "lws-cq"
		localQueueName     = "lws-lq"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "lws-e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		rf = testing.MakeResourceFlavor(resourceFlavorName).NodeLabel("instance-type", "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*testing.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllLeaderWorkerSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("LeaderWorkerSet created", func() {
		ginkgo.It("should admit group with leader only", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(1).
				Request(corev1.ResourceCPU, "100m").
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{
							Type:    "Available",
							Status:  metav1.ConditionTrue,
							Reason:  "AllGroupsReady",
							Message: "All replicas are ready",
						}, util.IgnoreConditionTimestampsAndObservedGeneration)),
					)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{
				Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"),
				Namespace: ns.Name,
			}
			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Delete the LeaderWorkerSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
			})
		})

		ginkgo.It("should admit group with leader and workers", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				Request(corev1.ResourceCPU, "100m").
				Queue(lq.Name).
				Obj()
			ginkgo.By("Create a LeaderWorkerSet", func() {
				gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{
							Type:    "Available",
							Status:  metav1.ConditionTrue,
							Reason:  "AllGroupsReady",
							Message: "All replicas are ready",
						}, util.IgnoreConditionTimestampsAndObservedGeneration)),
					)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: ns.Name}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Delete the LeaderWorkerSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
			})
		})

		ginkgo.It("should admit group with multiple leaders and workers that fits", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(2).
				Request(corev1.ResourceCPU, "100m").
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{
							Type:    "Available",
							Status:  metav1.ConditionTrue,
							Reason:  "AllGroupsReady",
							Message: "All replicas are ready",
						}, util.IgnoreConditionTimestampsAndObservedGeneration)),
					)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload1 := &kueue.Workload{}
			wlLookupKey1 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: ns.Name}
			ginkgo.By("Check workload for group 1 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
			})

			createdWorkload2 := &kueue.Workload{}
			wlLookupKey2 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "1"), Namespace: ns.Name}
			ginkgo.By("Check workload for group 2 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).To(gomega.Succeed())
			})

			ginkgo.By("Delete the LeaderWorkerSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workloads are deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload1, false, util.LongTimeout)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload2, false, util.LongTimeout)
			})
		})

		ginkgo.DescribeTable("should allow to scale up",
			func(startupPolicyType leaderworkersetv1.StartupPolicyType) {
				lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					Size(3).
					Replicas(1).
					Request(corev1.ResourceCPU, "100m").
					Queue(lq.Name).
					StartupPolicy(startupPolicyType).
					Obj()

				ginkgo.By("Create a LeaderWorkerSet", func() {
					gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())
				})

				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				ginkgo.By("Waiting for replicas is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{
								Type:    "Available",
								Status:  metav1.ConditionTrue,
								Reason:  "AllGroupsReady",
								Message: "All replicas are ready",
							}, util.IgnoreConditionTimestampsAndObservedGeneration)),
						)
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				createdWorkload1 := &kueue.Workload{}
				wlLookupKey1 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: ns.Name}
				ginkgo.By("Check workload is created", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				ginkgo.By("Scale up LeaderWorkerSet", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						createdLeaderWorkerSet.Spec.Replicas = ptr.To[int32](2)
						g.Expect(k8sClient.Update(ctx, createdLeaderWorkerSet)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Waiting for replicas is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{
								Type:    "Available",
								Status:  metav1.ConditionTrue,
								Reason:  "AllGroupsReady",
								Message: "All replicas are ready",
							}, util.IgnoreConditionTimestampsAndObservedGeneration)),
						)
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workload for group 1 is still exist", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				createdWorkload2 := &kueue.Workload{}
				wlLookupKey2 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "1"), Namespace: ns.Name}
				ginkgo.By("Check workload for group 2 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).To(gomega.Succeed())
				})

				ginkgo.By("Delete the LeaderWorkerSet", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
				})

				ginkgo.By("Check pods are deleted", func() {
					pods := &corev1.PodList{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
							leaderworkersetv1.SetNameLabelKey: lws.Name,
						}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
						g.Expect(pods.Items).To(gomega.BeEmpty())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workloads are deleted", func() {
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload1, false, util.LongTimeout)
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload2, false, util.LongTimeout)
				})
			},
			ginkgo.Entry("LeaderCreatedStartupPolicy", leaderworkersetv1.LeaderCreatedStartupPolicy),
			ginkgo.Entry("LeaderReadyStartupPolicy", leaderworkersetv1.LeaderReadyStartupPolicy),
		)

		ginkgo.DescribeTable("should allow to scale down",
			func(startupPolicyType leaderworkersetv1.StartupPolicyType) {
				lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					Size(3).
					Replicas(2).
					Request(corev1.ResourceCPU, "100m").
					Queue(lq.Name).
					StartupPolicy(startupPolicyType).
					Obj()

				ginkgo.By("Create a LeaderWorkerSet", func() {
					gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())
				})

				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				ginkgo.By("Waiting for replicas is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{
								Type:    "Available",
								Status:  metav1.ConditionTrue,
								Reason:  "AllGroupsReady",
								Message: "All replicas are ready",
							}, util.IgnoreConditionTimestampsAndObservedGeneration)),
						)
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				createdWorkload1 := &kueue.Workload{}
				wlLookupKey1 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: ns.Name}
				ginkgo.By("Check workload for group 1 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				createdWorkload2 := &kueue.Workload{}
				wlLookupKey2 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "1"), Namespace: ns.Name}
				ginkgo.By("Check workload for group 2 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).To(gomega.Succeed())
				})

				ginkgo.By("Scale down LeaderWorkerSet", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						createdLeaderWorkerSet.Spec.Replicas = ptr.To[int32](1)
						g.Expect(k8sClient.Update(ctx, createdLeaderWorkerSet)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Waiting for replicas is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{
								Type:    "Available",
								Status:  metav1.ConditionTrue,
								Reason:  "AllGroupsReady",
								Message: "All replicas are ready",
							}, util.IgnoreConditionTimestampsAndObservedGeneration)),
						)
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workload for group 1 is still exist", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				ginkgo.By("Check workload for group 2 is deleted", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, createdWorkload2, false)
				})

				ginkgo.By("Delete the LeaderWorkerSet", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
				})

				ginkgo.By("Check pods are deleted", func() {
					pods := &corev1.PodList{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
							leaderworkersetv1.SetNameLabelKey: lws.Name,
						}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
						g.Expect(pods.Items).To(gomega.BeEmpty())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workloads are deleted", func() {
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload1, false, util.LongTimeout)
				})
			},
			ginkgo.Entry("LeaderCreatedStartupPolicy", leaderworkersetv1.LeaderCreatedStartupPolicy),
			ginkgo.Entry("LeaderReadyStartupPolicy", leaderworkersetv1.LeaderReadyStartupPolicy),
		)

		ginkgo.DescribeTable("should admit group with multiple leaders and workers that fits and have different resource needs",
			func(startupPolicyType leaderworkersetv1.StartupPolicyType) {
				lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					Size(3).
					Replicas(2).
					Request(corev1.ResourceCPU, "100m").
					Queue(lq.Name).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "c",
									Args:  util.BehaviorWaitForDeletion,
									Image: util.E2eTestAgnHostImage,
									Resources: corev1.ResourceRequirements{
										Requests: corev1.ResourceList{
											corev1.ResourceCPU: resource.MustParse("150m"),
										},
									},
								},
							},
							NodeSelector: map[string]string{},
						},
					}).
					StartupPolicy(startupPolicyType).
					Obj()

				ginkgo.By("Create a LeaderWorkerSet", func() {
					gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())
				})

				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				ginkgo.By("Waiting for replicas is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
							gomega.BeComparableTo(metav1.Condition{
								Type:    "Available",
								Status:  metav1.ConditionTrue,
								Reason:  "AllGroupsReady",
								Message: "All replicas are ready",
							}, util.IgnoreConditionTimestampsAndObservedGeneration)),
						)
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				createdWorkload1 := &kueue.Workload{}
				wlLookupKey1 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: ns.Name}
				ginkgo.By("Check workload for group 1 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				createdWorkload2 := &kueue.Workload{}
				wlLookupKey2 := types.NamespacedName{Name: leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: ns.Name}
				ginkgo.By("Check workload for group 2 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).To(gomega.Succeed())
				})

				ginkgo.By("Delete the LeaderWorkerSet", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
				})

				ginkgo.By("Check pods are deleted", func() {
					pods := &corev1.PodList{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
							leaderworkersetv1.SetNameLabelKey: lws.Name,
						}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
						g.Expect(pods.Items).To(gomega.BeEmpty())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workloads are deleted", func() {
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload1, false, util.LongTimeout)
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload2, false, util.LongTimeout)
				})
			},
			ginkgo.Entry("LeaderCreatedStartupPolicy", leaderworkersetv1.LeaderCreatedStartupPolicy),
			ginkgo.Entry("LeaderReadyStartupPolicy", leaderworkersetv1.LeaderReadyStartupPolicy),
		)

		ginkgo.It("should allow to update the PodTemplate in LeaderWorkerSet", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(2).
				Request(corev1.ResourceCPU, "100m").
				Queue(lq.Name).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Args:  util.BehaviorWaitForDeletion,
								Image: util.E2eTestAgnHostImageOld,
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("150m"),
									},
								},
							},
						},
						NodeSelector: map[string]string{},
					},
				}).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				gomega.Expect(k8sClient.Create(ctx, lws)).To(gomega.Succeed())
			})

			createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

			ginkgo.By("Waiting for replicas is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{
							Type:    "Available",
							Status:  metav1.ConditionTrue,
							Reason:  "AllGroupsReady",
							Message: "All replicas are ready",
						}, util.IgnoreConditionTimestampsAndObservedGeneration)),
					)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Update the PodTemplate in LeaderWorkerSet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers).Should(gomega.HaveLen(1))
					createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Image = util.E2eTestAgnHostImage
					g.Expect(createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers).Should(gomega.HaveLen(1))
					createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = util.E2eTestAgnHostImage
					g.Expect(k8sClient.Update(ctx, createdLeaderWorkerSet)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the Pods to be replaced with the new template", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(map[string]string{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(6))
					for _, p := range pods.Items {
						g.Expect(p.Spec.Containers[0].Image).To(gomega.Equal(util.E2eTestAgnHostImage))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready again", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{
							Type:    "Available",
							Status:  metav1.ConditionTrue,
							Reason:  "AllGroupsReady",
							Message: "All replicas are ready",
						}, util.IgnoreConditionTimestampsAndObservedGeneration)),
					)
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload1 := &kueue.Workload{}
			wlLookupKey1 := types.NamespacedName{
				Name:      leaderworkerset.GetWorkloadName(createdLeaderWorkerSet.UID, createdLeaderWorkerSet.Name, "0"),
				Namespace: ns.Name,
			}
			ginkgo.By("Check workload for group 1 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
			})

			createdWorkload2 := &kueue.Workload{}
			wlLookupKey2 := types.NamespacedName{
				Name:      leaderworkerset.GetWorkloadName(createdLeaderWorkerSet.UID, createdLeaderWorkerSet.Name, "0"),
				Namespace: ns.Name,
			}
			ginkgo.By("Check workload for group 2 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)).To(gomega.Succeed())
			})

			ginkgo.By("Delete the LeaderWorkerSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			})

			ginkgo.By("Check pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload1, false, util.LongTimeout)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload2, false, util.LongTimeout)
			})
		})
	})
})
