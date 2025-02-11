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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Stateful set integration", func() {
	var (
		ns               *corev1.Namespace
		onDemandRF       *kueue.ResourceFlavor
		RFName           = "stateful-set-resource-flavour"
		clusterQueueName = "stateful-set-cluster-queue"
		localQueueName   = "stateful-set-local-queue"
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "stateful-set-e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		onDemandRF = testing.MakeResourceFlavor(RFName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandRF)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
	})

	ginkgo.When("Single CQ", func() {
		var (
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*testing.MakeFlavorQuotas(RFName).
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
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should admit group that fits", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sf", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Request(corev1.ResourceCPU, "1").
				Limit(corev1.ResourceCPU, "1").
				Replicas(3).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}
			gomega.Expect(k8sClient.Create(ctx, statefulSet)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).
					To(gomega.Succeed())
				g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			createdWorkload := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			ginkgo.By("Creating potentially conflicting stateful-set", func() {
				conflictingStatefulSet := statefulsettesting.MakeStatefulSet("sf-conflict", ns.Name).
					Image(util.E2eTestSleepImage, []string{"10m"}).
					Request(corev1.ResourceCPU, "0.1").
					Limit(corev1.ResourceCPU, "0.1").
					Replicas(1).
					Queue(lq.Name).
					Obj()
				conflictingWlLookupKey := types.NamespacedName{
					Name:      statefulset.GetWorkloadName(conflictingStatefulSet.Name),
					Namespace: ns.Name,
				}
				gomega.Expect(k8sClient.Create(ctx, conflictingStatefulSet)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conflictingStatefulSet), createdStatefulSet)).
						To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				conflictingWorkload := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, conflictingWlLookupKey, conflictingWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Name).ToNot(gomega.Equal(conflictingWorkload.Name))
				util.ExpectObjectToBeDeleted(ctx, k8sClient, conflictingStatefulSet, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, conflictingWorkload, false, util.LongTimeout)
			})

			util.ExpectObjectToBeDeleted(ctx, k8sClient, statefulSet, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)
		})

		ginkgo.It("should delete all Pods if StatefulSet was deleted after being partially ready", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Request(corev1.ResourceCPU, "100m").
				Replicas(3).
				Queue(localQueueName).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				gomega.Expect(k8sClient.Create(ctx, statefulSet)).To(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("Waiting for replicas is partially ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.BeNumerically(">", 0))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete StatefulSet", func() {
				gomega.Expect(k8sClient.Delete(ctx, statefulSet)).To(gomega.Succeed())
			})

			ginkgo.By("Check all pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
