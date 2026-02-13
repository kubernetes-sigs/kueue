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
	"fmt"
	"strconv"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("LeaderWorkerSet integration", ginkgo.Label("area:singlecluster", "feature:leaderworkerset"), func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lws-e2e-")
		resourceFlavorName = "lws-rf-" + ns.Name
		clusterQueueName = "lws-cq-" + ns.Name
		localQueueName = "lws-lq-" + ns.Name

		rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).NodeLabel("instance-type", "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteAllLeaderWorkerSetsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("LeaderWorkerSet created", func() {
		ginkgo.It("should admit group with leader only", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload)).To(gomega.Succeed())
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
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()
			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload)).To(gomega.Succeed())
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
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(2).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload1 := &kueue.Workload{}
			ginkgo.By("Check workload for group 1 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload1)).To(gomega.Succeed())
			})

			createdWorkload2 := &kueue.Workload{}
			ginkgo.By("Check workload for group 2 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "1"), createdWorkload2)).To(gomega.Succeed())
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
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					Size(3).
					Replicas(1).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					TerminationGracePeriod(1).
					Queue(lq.Name).
					StartupPolicy(startupPolicyType).
					Obj()

				ginkgo.By("Create a LeaderWorkerSet", func() {
					util.MustCreate(ctx, k8sClient, lws)
				})

				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				ginkgo.By("Waiting for replicas to be ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				createdWorkload1 := &kueue.Workload{}
				wlLookupKey1 := util.WorkloadKeyForLeaderWorkerSet(lws, "0")
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

				ginkgo.By("Waiting for replicas to be ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workload for group 1 is still exist", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				createdWorkload2 := &kueue.Workload{}
				wlLookupKey2 := util.WorkloadKeyForLeaderWorkerSet(lws, "1")
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
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload1, false, util.VeryLongTimeout)
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload2, false, util.VeryLongTimeout)
				})
			},
			ginkgo.Entry("LeaderCreatedStartupPolicy", leaderworkersetv1.LeaderCreatedStartupPolicy),
			ginkgo.Entry("LeaderReadyStartupPolicy", leaderworkersetv1.LeaderReadyStartupPolicy),
		)

		ginkgo.DescribeTable("should allow to scale down",
			func(startupPolicyType leaderworkersetv1.StartupPolicyType) {
				lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					Size(3).
					Replicas(2).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					TerminationGracePeriod(1).
					Queue(lq.Name).
					StartupPolicy(startupPolicyType).
					Obj()

				ginkgo.By("Create a LeaderWorkerSet", func() {
					util.MustCreate(ctx, k8sClient, lws)
				})

				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				ginkgo.By("Waiting for replicas to be ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				createdWorkload1 := &kueue.Workload{}
				wlLookupKey1 := util.WorkloadKeyForLeaderWorkerSet(lws, "0")
				ginkgo.By("Check workload for group 1 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload1)).To(gomega.Succeed())
				})

				createdWorkload2 := &kueue.Workload{}
				wlLookupKey2 := util.WorkloadKeyForLeaderWorkerSet(lws, "1")
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

				ginkgo.By("Waiting for replicas to be ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workload for group 1 is still exist", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				ginkgo.By("Check workload for group 2 is released", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						err := k8sClient.Get(ctx, wlLookupKey2, createdWorkload2)
						if client.IgnoreNotFound(err) != nil {
							gomega.Expect(err).To(gomega.Succeed())
						}
						if err == nil {
							g.Expect(createdWorkload2.OwnerReferences).To(gomega.BeEmpty())
						}
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
					util.ExpectObjectToBeDeleted(ctx, k8sClient, createdWorkload2, true)
				})
			},
			ginkgo.Entry("LeaderCreatedStartupPolicy", leaderworkersetv1.LeaderCreatedStartupPolicy),
			ginkgo.Entry("LeaderReadyStartupPolicy", leaderworkersetv1.LeaderReadyStartupPolicy),
		)

		ginkgo.DescribeTable("should allow to scale up, scale down fast",
			func(startupPolicyType leaderworkersetv1.StartupPolicyType) {
				lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					Size(3).
					Replicas(1).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					TerminationGracePeriod(1).
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
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				createdWorkload1 := &kueue.Workload{}
				wlLookupKey1 := util.WorkloadKeyForLeaderWorkerSet(lws, "0")
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
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Check workload for group 1 is still exist", func() {
					gomega.Expect(k8sClient.Get(ctx, wlLookupKey1, createdWorkload1)).To(gomega.Succeed())
				})

				createdWorkload2 := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "1"),
						Namespace: ns.Name,
					},
				}
				ginkgo.By("Check workload for group 2 is released", func() {
					util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload2, false, util.LongTimeout)
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
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					Size(3).
					Replicas(2).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					TerminationGracePeriod(1).
					Queue(lq.Name).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "c",
									Args:  util.BehaviorWaitForDeletion,
									Image: util.GetAgnHostImage(),
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
					TerminationGracePeriod(1).
					Obj()

				ginkgo.By("Create a LeaderWorkerSet", func() {
					util.MustCreate(ctx, k8sClient, lws)
				})

				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				ginkgo.By("Waiting for replicas to be ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
						g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
						g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})

				createdWorkload1 := &kueue.Workload{}
				ginkgo.By("Check workload for group 1 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload1)).To(gomega.Succeed())
				})

				createdWorkload2 := &kueue.Workload{}
				ginkgo.By("Check workload for group 2 is created", func() {
					gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "1"), createdWorkload2)).To(gomega.Succeed())
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
				Image(util.GetAgnHostImageOld(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(2).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Queue(lq.Name).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Args:  util.BehaviorWaitForDeletion,
								Image: util.GetAgnHostImageOld(),
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
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

			ginkgo.By("Waiting for replicas to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Update the PodTemplate in LeaderWorkerSet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers).Should(gomega.HaveLen(1))
					createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.LeaderTemplate.Spec.Containers[0].Image = util.GetAgnHostImage()
					g.Expect(createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers).Should(gomega.HaveLen(1))
					createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = util.GetAgnHostImage()
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
						g.Expect(p.Spec.Containers[0].Image).To(gomega.Equal(util.GetAgnHostImage()))
					}
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas to be ready again", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload1 := &kueue.Workload{}
			ginkgo.By("Check workload for group 1 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload1)).To(gomega.Succeed())
			})

			createdWorkload2 := &kueue.Workload{}
			ginkgo.By("Check workload for group 2 is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "1"), createdWorkload2)).To(gomega.Succeed())
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

		ginkgo.It("Rolling update with maxSurge creates workloads for surge pods and completes successfully", func() {
			const (
				lwsReplicas    = 4
				maxSurge       = 2
				maxUnavailable = 2
			)

			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws-rollout", ns.Name).
				Image(util.GetAgnHostImageOld(), util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(lwsReplicas).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Queue(lq.Name).
				TerminationGracePeriod(1).
				Obj()

			lws.Spec.RolloutStrategy = leaderworkersetv1.RolloutStrategy{
				Type: leaderworkersetv1.RollingUpdateStrategyType,
				RollingUpdateConfiguration: &leaderworkersetv1.RollingUpdateConfiguration{
					MaxUnavailable: intstr.FromInt32(maxUnavailable),
					MaxSurge:       intstr.FromInt32(maxSurge),
				},
			}

			ginkgo.By("Create LeaderWorkerSet with rolling update strategy", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

			ginkgo.By(fmt.Sprintf("Wait for initial %d replicas to be ready", lwsReplicas), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(lwsReplicas)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Verify workloads exist for initial %d groups", lwsReplicas), func() {
				wl := &kueue.Workload{}
				for i := range lwsReplicas {
					gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, strconv.Itoa(i)), wl)).To(gomega.Succeed())
				}
			})

			ginkgo.By("Trigger rolling update by changing image", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					createdLeaderWorkerSet.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Image = util.GetAgnHostImage()
					g.Expect(k8sClient.Update(ctx, createdLeaderWorkerSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Wait for the surge workloads to be created with maxSurge=%d", maxSurge), func() {
				wl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					for i := lwsReplicas; i < lwsReplicas+maxSurge; i++ {
						g.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, strconv.Itoa(i)), wl)).To(gomega.Succeed(),
							"workload for surge group %d should exist", i)
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Verify rolling update completes successfully with %d replicas running", lwsReplicas), func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.UpdatedReplicas).To(gomega.Equal(int32(lwsReplicas)))
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(lwsReplicas)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))

					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
						client.MatchingLabels{leaderworkersetv1.SetNameLabelKey: lws.Name})).To(gomega.Succeed())

					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By(fmt.Sprintf("Verify surge workloads are cleaned up with maxSurge=%d", maxSurge), func() {
				for i := lwsReplicas; i < lwsReplicas+maxSurge; i++ {
					wl := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, strconv.Itoa(i)), wl)).
							To(utiltesting.BeNotFoundError())
					}, util.Timeout, util.Interval).Should(gomega.Succeed(),
						"surge workload for group %d should be deleted after rollout completes", i)
				}
			})

			ginkgo.By("Delete the LeaderWorkerSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			})
		})
	})

	ginkgo.When("LeaderWorkerSet created with WorkloadPriorityClass", func() {
		var (
			highPriorityWPC *kueue.WorkloadPriorityClass
			lowPriorityWPC  *kueue.WorkloadPriorityClass
		)

		ginkgo.BeforeEach(func() {
			highPriorityWPC = utiltestingapi.MakeWorkloadPriorityClass("high-priority-" + ns.Name).
				PriorityValue(5000).
				Obj()
			util.MustCreate(ctx, k8sClient, highPriorityWPC)

			lowPriorityWPC = utiltestingapi.MakeWorkloadPriorityClass("low-priority-" + ns.Name).
				PriorityValue(1000).
				Obj()
			util.MustCreate(ctx, k8sClient, lowPriorityWPC)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityWPC, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityWPC, true)
		})

		ginkgo.It("should allow to update the PodTemplate in LeaderWorkerSet", func() {
			lowPriorityLWS := leaderworkersettesting.MakeLeaderWorkerSet("low-priority", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				WorkloadPriorityClass(lowPriorityWPC.Name).
				Obj()
			ginkgo.By("Create a low priority LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lowPriorityLWS)
			})

			ginkgo.By("Waiting for replicas to be ready in low priority LeaderWorkerSet", func() {
				createdLowPriorityLWS := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityLWS), createdLowPriorityLWS)).To(gomega.Succeed())
					g.Expect(createdLowPriorityLWS.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLowPriorityLWS.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdLowPriorityWl := &kueue.Workload{}
			lowPriorityWlLookupKey := util.WorkloadKeyForLeaderWorkerSet(lowPriorityLWS, "0")

			ginkgo.By("Verify workload is created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, lowPriorityWPC.Name, lowPriorityWPC.Value, lowPriorityWlLookupKey)
			})

			ginkgo.By("Check the low priority Workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lowPriorityWlLookupKey, createdLowPriorityWl)).To(gomega.Succeed())
					g.Expect(createdLowPriorityWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			highPriorityLWS := leaderworkersettesting.MakeLeaderWorkerSet("high-priority", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				WorkloadPriorityClass(highPriorityWPC.Name).
				Obj()
			ginkgo.By("Create a high priority LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, highPriorityLWS)
			})

			ginkgo.By("Waiting for ready replicas in the high priority LeaderWorkerSet", func() {
				createdHighPriorityLWS := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(highPriorityLWS), createdHighPriorityLWS)).To(gomega.Succeed())
					g.Expect(createdHighPriorityLWS.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdHighPriorityLWS.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the low-priory LeaderWorkerSet to be preempted", func() {
				createdLowPriorityLWS := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityLWS), createdLowPriorityLWS)).To(gomega.Succeed())
					g.Expect(createdLowPriorityLWS.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
					g.Expect(createdLowPriorityLWS.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Progressing", "GroupsProgressing"))
					g.Expect(createdLowPriorityLWS.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdHighPriorityWl := &kueue.Workload{}
			highPriorityWlLookupKey := util.WorkloadKeyForLeaderWorkerSet(highPriorityLWS, "0")

			ginkgo.By("Verify the high priority Workload created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highPriorityWPC.Name, highPriorityWPC.Value, highPriorityWlLookupKey)
			})

			ginkgo.By("Await for the high priority Workload to be admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, highPriorityWlLookupKey, createdHighPriorityWl)).To(gomega.Succeed())
					g.Expect(createdHighPriorityWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the low priority Workload is preempted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lowPriorityWlLookupKey, createdLowPriorityWl)).To(gomega.Succeed())
					g.Expect(createdLowPriorityWl.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadAdmitted))
					g.Expect(createdLowPriorityWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to update the workload priority in LeaderWorkerSet", func() {
			lowPriorityLWS := leaderworkersettesting.MakeLeaderWorkerSet("low-priority", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				WorkloadPriorityClass(lowPriorityWPC.Name).
				Obj()
			ginkgo.By("Create a low priority LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lowPriorityLWS)
			})

			ginkgo.By("Waiting for replicas is ready in low priority LeaderWorkerSet", func() {
				createdLowPriorityLWS := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityLWS), createdLowPriorityLWS)).To(gomega.Succeed())
					g.Expect(createdLowPriorityLWS.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLowPriorityLWS.Status.Conditions).To(gomega.ContainElement(
						gomega.BeComparableTo(metav1.Condition{
							Type:   "Available",
							Status: metav1.ConditionTrue,
							Reason: "AllGroupsReady",
						}, util.IgnoreConditionTimestampsAndObservedGeneration, util.IgnoreConditionMessage),
					))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdLowPriorityWl := &kueue.Workload{}
			lowPriorityWlLookupKey := util.WorkloadKeyForLeaderWorkerSet(lowPriorityLWS, "0")

			ginkgo.By("Verify the low priority Workload created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, lowPriorityWPC.Name, lowPriorityWPC.Value, lowPriorityWlLookupKey)
			})

			ginkgo.By("Check the low priority Workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lowPriorityWlLookupKey, createdLowPriorityWl)).To(gomega.Succeed())
					g.Expect(createdLowPriorityWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			updatablePriorityLWS := leaderworkersettesting.MakeLeaderWorkerSet("updatable-priority", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				WorkloadPriorityClass(lowPriorityWPC.Name).
				Obj()
			ginkgo.By("Create another low priority LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, updatablePriorityLWS)
			})

			createdUpdatablePriorityWl := &kueue.Workload{}
			updatablePriorityWlLookupKey := util.WorkloadKeyForLeaderWorkerSet(updatablePriorityLWS, "0")

			ginkgo.By("Verify another low priority Workload created with workload priority class", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, lowPriorityWPC.Name, lowPriorityWPC.Value, updatablePriorityWlLookupKey)
			})

			ginkgo.By("Updating priority of second LeaderWorkerSet to high-priority", func() {
				createdHighPriorityLWS := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(updatablePriorityLWS), createdHighPriorityLWS)).To(gomega.Succeed())
					createdHighPriorityLWS.Labels[ctrlconstants.WorkloadPriorityClassLabel] = highPriorityWPC.Name
					g.Expect(k8sClient.Update(ctx, createdHighPriorityLWS)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the Workload priority is updated", func() {
				util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, highPriorityWPC.Name, highPriorityWPC.Value, updatablePriorityWlLookupKey)
			})

			ginkgo.By("Await for the low-priority Workload to be preempted", func() {
				createdLowPriorityWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lowPriorityWlLookupKey, createdLowPriorityWl)).To(gomega.Succeed())
					g.Expect(createdLowPriorityWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the low-priority LeaderWorkerSet to be preempted", func() {
				createdLowPriorityLWS := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityLWS), createdLowPriorityLWS)).To(gomega.Succeed())
					g.Expect(createdLowPriorityLWS.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
					g.Expect(createdLowPriorityLWS.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(metav1.Condition{
							Type:   "Progressing",
							Status: metav1.ConditionTrue,
							Reason: "GroupsProgressing",
						}, util.IgnoreConditionTimestampsAndObservedGeneration, util.IgnoreConditionMessage),
						gomega.BeComparableTo(metav1.Condition{
							Type:   "Available",
							Status: metav1.ConditionFalse,
							Reason: "AllGroupsReady",
						}, util.IgnoreConditionTimestampsAndObservedGeneration, util.IgnoreConditionMessage),
					))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the high priority Workload to be admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, updatablePriorityWlLookupKey, createdUpdatablePriorityWl)).To(gomega.Succeed())
					g.Expect(createdUpdatablePriorityWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("LeaderWorkerSet created with Restart Policy", func() {
		ginkgo.It("should recreate pods when policy is set to RecreateGroupOnPodRestart", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				RestartPolicy(leaderworkersetv1.RecreateGroupOnPodRestart).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload)).To(gomega.Succeed())
			})

			var podToDelete *corev1.Pod
			originalPodUIDs := make(map[string]types.UID)
			ginkgo.By("Select a worker pod to delete", func() {
				pods := &corev1.PodList{}
				gomega.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
					leaderworkersetv1.SetNameLabelKey: lws.Name,
				}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
				gomega.Expect(pods.Items).To(gomega.HaveLen(3))

				for i, pod := range pods.Items {
					originalPodUIDs[pod.Name] = pod.UID
					if pod.Labels[leaderworkersetv1.WorkerIndexLabelKey] != "0" {
						podToDelete = &pods.Items[i]
					}
				}
				gomega.Expect(podToDelete).NotTo(gomega.BeNil(), "Couldn't find a worker pod to delete")
			})

			ginkgo.By("Delete the worker pod", func() {
				gomega.Expect(k8sClient.Delete(ctx, podToDelete)).Should(gomega.Succeed())
			})

			ginkgo.By("Wait for all pods to be recreated with new UIDs", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())

					recreatedCount := 0
					for _, pod := range pods.Items {
						originalUID, existed := originalPodUIDs[pod.Name]
						if existed && pod.UID != originalUID {
							recreatedCount++
						}
					}
					g.Expect(recreatedCount).To(gomega.Equal(3), "all pods should be recreated with new UIDs due to RecreateGroupOnPodRestart policy")
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify all pods are running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should only recreate the deleted pod when policy is set to NoneRestartPolicy", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(lq.Name).
				RestartPolicy(leaderworkersetv1.NoneRestartPolicy).
				Obj()

			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload)).To(gomega.Succeed())
			})

			var podToDelete *corev1.Pod
			originalPodUIDs := make(map[string]types.UID)
			ginkgo.By("Select a worker pod to delete", func() {
				pods := &corev1.PodList{}
				gomega.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
					leaderworkersetv1.SetNameLabelKey: lws.Name,
				}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
				gomega.Expect(pods.Items).To(gomega.HaveLen(3))

				for i, pod := range pods.Items {
					originalPodUIDs[pod.Name] = pod.UID
					if pod.Labels[leaderworkersetv1.WorkerIndexLabelKey] != "0" {
						podToDelete = &pods.Items[i]
					}
				}
				gomega.Expect(podToDelete).NotTo(gomega.BeNil(), "Couldn't find a worker pod to delete")
			})

			deletedPodName := podToDelete.Name
			ginkgo.By("Delete the worker pod", func() {
				gomega.Expect(k8sClient.Delete(ctx, podToDelete)).Should(gomega.Succeed())
			})

			ginkgo.By("Verify if only the deleted pod was recreated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())

					recreatedCount := 0
					unchangedCount := 0
					for _, pod := range pods.Items {
						originalUID, existed := originalPodUIDs[pod.Name]
						if existed {
							if pod.UID != originalUID {
								recreatedCount++
								g.Expect(pod.Name).To(gomega.Equal(deletedPodName), "only the deleted pod should be recreated")
							} else {
								unchangedCount++
							}
						}
					}
					g.Expect(recreatedCount).To(gomega.Equal(1), "only the deleted pod should be recreated with NoneRestartPolicy")
					g.Expect(unchangedCount).To(gomega.Equal(2), "other pods should NOT be recreated with NoneRestartPolicy")
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify all pods are running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Workload deactivated", func() {
		ginkgo.It("shouldn't delete deactivated Workload", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
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
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := util.WorkloadKeyForLeaderWorkerSet(lws, "0")
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			createdWorkloadUID := createdWorkload.UID

			ginkgo.By("Deactivate workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					createdWorkload.Spec.Active = ptr.To(false)
					g.Expect(k8sClient.Update(ctx, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas to not be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check pods are gated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{
							Name: podconstants.SchedulingGateName,
						}))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deactivated", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.UID).Should(gomega.Equal(createdWorkloadUID))
					g.Expect(createdWorkload.Spec.Active).Should(gomega.Equal(ptr.To(false)))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Activate workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					createdWorkload.Spec.Active = ptr.To(true)
					g.Expect(k8sClient.Update(ctx, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason("Available", "AllGroupsReady"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.UID).Should(gomega.Equal(createdWorkloadUID))
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
	})
})
