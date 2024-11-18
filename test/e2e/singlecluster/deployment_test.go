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
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
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
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "deployment-e2e-",
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

	ginkgo.It("should admit workloads that fits", func() {
		deployment := deploymenttesting.MakeDeployment("deployment", ns.Name).
			Image(util.E2eTestSleepImage, []string{"10m"}).
			Request(corev1.ResourceCPU, "100m").
			Replicas(3).
			Queue(lq.Name).
			Obj()

		ginkgo.By("Create a deployment", func() {
			gomega.Expect(k8sClient.Create(ctx, deployment)).To(gomega.Succeed())
		})

		ginkgo.By("Wait for replicas ready", func() {
			createdDeployment := &appsv1.Deployment{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).To(gomega.Succeed())
				g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		pods := &corev1.PodList{}
		gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Namespace),
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
})
