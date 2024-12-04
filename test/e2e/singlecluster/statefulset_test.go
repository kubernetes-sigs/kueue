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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("StatefulSet integration", func() {
	const (
		resourceFlavorName = "sts-rf"
		clusterQueueName   = "sts-cq"
		localQueueName     = "sts-lq"
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
				GenerateName: "sts-e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		rf = testing.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
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
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("StatefulSet created", func() {
		ginkgo.It("should admit group that fits", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Request(corev1.ResourceCPU, "100m").
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
				conflictingStatefulSet := statefulsettesting.MakeStatefulSet("sts-conflict", ns.Name).
					Image(util.E2eTestSleepImage, []string{"10m"}).
					Request(corev1.ResourceCPU, "100m").
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

		ginkgo.It("should allow to update the PodTemplate in StatefulSet", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.E2eTestSleepImageOld, []string{"10m"}).
				Request(corev1.ResourceCPU, "100m").
				Replicas(3).
				Queue(lq.Name).
				Obj()
			ginkgo.By("Create a StatefulSet", func() {
				gomega.Expect(k8sClient.Create(ctx, statefulSet)).To(gomega.Succeed())
			})

			createdStatefulSet := &appsv1.StatefulSet{}
			ginkgo.By("Waiting for replicas is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).
						To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Update the PodTemplate in StatefulSet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Spec.Containers).Should(gomega.HaveLen(1))
					createdStatefulSet.Spec.Template.Spec.Containers[0].Image = util.E2eTestSleepImage
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the Pods to be replaced with the new template", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(createdStatefulSet.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, p := range pods.Items {
						g.Expect(createdStatefulSet.Spec.Template.Spec.Containers).Should(gomega.HaveLen(1))
						g.Expect(p.Spec.Containers[0].Image).To(gomega.Equal(util.E2eTestSleepImage))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the StatefulSet to have all replicas ready again", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).
						To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should delete all pods on scale down to zero", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Request(corev1.ResourceCPU, "100m").
				Replicas(3).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				gomega.Expect(k8sClient.Create(ctx, statefulSet)).To(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Scale down replicas to zero", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](0)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, createdWorkload, false)
			})
		})

		ginkgo.It("should create pods after scale up from zero", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Request(corev1.ResourceCPU, "100m").
				Replicas(0).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				gomega.Expect(k8sClient.Create(ctx, statefulSet)).To(gomega.Succeed())
			})

			ginkgo.By("Scale up replicas", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](3)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is created", func() {
				createdWorkload := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to scale up after scale down to zero", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.E2eTestSleepImage, []string{"10m"}).
				Request(corev1.ResourceCPU, "100m").
				Replicas(3).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				gomega.Expect(k8sClient.Create(ctx, statefulSet)).To(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Scale down replicas to zero", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](0)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Wait for ReadyReplicas < 3", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.BeNumerically("<", 3))
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Scale up replicas to zero - retry as it may not be possible immediately", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](3)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
