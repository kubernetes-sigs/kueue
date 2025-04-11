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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/testing"
	deploymenttesting "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Deployment", func() {
	const (
		resourceFlavorName = "deployment-rf"
		clusterQueueName   = "deployment-cq"
		localQueueName     = "deployment-lq"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "deployment-e2e-")

		rf = testing.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		util.MustCreate(ctx, k8sClient, rf)

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
		util.MustCreate(ctx, k8sClient, cq)

		lq = testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("should admit workloads that fits", func() {
		deployment := deploymenttesting.MakeDeployment("deployment", ns.Name).
			Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1).
			Replicas(3).
			Queue(lq.Name).
			Obj()

		ginkgo.By("Create a deployment", func() {
			util.MustCreate(ctx, k8sClient, deployment)
		})

		ginkgo.By("Wait for replicas ready", func() {
			createdDeployment := &appsv1.Deployment{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
				g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		pods := &corev1.PodList{}
		gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
			client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())

		createdWorkloads := make([]*kueue.Workload, 0, len(pods.Items))
		ginkgo.By("Check that workloads are created and admitted", func() {
			for _, p := range pods.Items {
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{
					Name:      pod.GetWorkloadNameForPod(p.Name, p.UID),
					Namespace: p.Namespace,
				}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				createdWorkloads = append(createdWorkloads, createdWorkload)
			}
		})

		ginkgo.By("Delete the deployment", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deployment, true)
		})

		ginkgo.By("Check that workloads are deleted", func() {
			for _, wl := range createdWorkloads {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, wl, false, util.LongTimeout)
			}
		})
	})

	ginkgo.It("should admit workloads after change queue-name if AvailableReplicas = 0", func() {
		deployment := deploymenttesting.MakeDeployment("deployment", ns.Name).
			Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
			RequestAndLimit(corev1.ResourceCPU, "200m").
			TerminationGracePeriod(1).
			Replicas(3).
			Queue("invalid-queue-name").
			Obj()

		ginkgo.By("Create a deployment", func() {
			util.MustCreate(ctx, k8sClient, deployment)
		})

		ginkgo.By("Wait for replicas unavailable", func() {
			createdDeployment := &appsv1.Deployment{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
				g.Expect(createdDeployment.Status.Replicas).To(gomega.Equal(int32(3)))
				g.Expect(createdDeployment.Status.UnavailableReplicas).To(gomega.Equal(int32(3)))
				g.Expect(createdDeployment.Status.AvailableReplicas).To(gomega.Equal(int32(0)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		pods := &corev1.PodList{}
		gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
			client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())

		createdWorkloads := make([]*kueue.Workload, 0, len(pods.Items))
		ginkgo.By("Check that workloads are created but not admitted", func() {
			for _, p := range pods.Items {
				gomega.Expect(utilpod.HasGate(&p, podconstants.SchedulingGateName)).Should(gomega.BeTrue())
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{
					Name:      pod.GetWorkloadNameForPod(p.Name, p.UID),
					Namespace: p.Namespace,
				}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
				createdWorkloads = append(createdWorkloads, createdWorkload)
			}
		})

		ginkgo.By("Update queue-name on the deployment", func() {
			createdDeployment := &appsv1.Deployment{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
				createdDeployment.Labels[constants.QueueLabel] = lq.Name
				g.Expect(k8sClient.Update(ctx, createdDeployment)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Wait for replicas ready", func() {
			createdDeployment := &appsv1.Deployment{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
				g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Check previous pods are deleted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
				g.Expect(pods.Items).To(gomega.HaveLen(3))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Check that workloads are created and admitted", func() {
			gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
				client.MatchingLabels(deployment.Spec.Selector.MatchLabels))).To(gomega.Succeed())
			for _, p := range pods.Items {
				createdWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{
					Name:      pod.GetWorkloadNameForPod(p.Name, p.UID),
					Namespace: p.Namespace,
				}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				createdWorkloads = append(createdWorkloads, createdWorkload)
			}
		})

		ginkgo.By("Delete the deployment", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deployment, true)
		})

		ginkgo.By("Check that workloads are deleted", func() {
			for _, wl := range createdWorkloads {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, wl, false, util.LongTimeout)
			}
		})
	})
})
