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

package jobs

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	deploymentcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/deployment"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Deployment Webhook", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns         *corev1.Namespace
		deployment *appsv1.Deployment
	)

	ginkgo.BeforeAll(func() {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
		err = serverVersionFetcher.FetchServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		fwk.StartManager(ctx, cfg, managerSetup(
			deploymentcontroller.SetupWebhook,
			jobframework.WithKubeServerVersion(serverVersionFetcher),
		))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "deployment-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("the queue-name label is set", func() {
		ginkgo.BeforeEach(func() {
			ginkgo.By("Create deployment", func() {
				deployment = testingdeployment.MakeDeployment("deployment", ns.Name).
					Queue("user-queue").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, deployment)).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should inject queue name to pod template labels", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdDeployment := &appsv1.Deployment{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
				g.Expect(createdDeployment.Spec.Template.Labels[constants.QueueLabel]).
					To(
						gomega.Equal("user-queue"),
						"Queue name should be injected to pod template labels",
					)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should allow to change the queue name (ReadyReplicas = 0)", func() {
			createdDeployment := &appsv1.Deployment{}

			ginkgo.By("Change queue name", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					deploymentWrapper := &testingdeployment.DeploymentWrapper{Deployment: *createdDeployment}
					updatedDeployment := deploymentWrapper.Queue("another-queue").Obj()
					g.Expect(k8sClient.Update(ctx, updatedDeployment)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check queue name is injected to pod template label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					g.Expect(createdDeployment.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.Equal("another-queue"),
							"Queue name should be injected to pod template labels",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("shouldn't allow to remove the queue label", func() {
			createdDeployment := &appsv1.Deployment{}

			ginkgo.By("Try to remove queue label", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
				delete(createdDeployment.Labels, constants.QueueLabel)
				gomega.Expect(k8sClient.Update(ctx, createdDeployment)).To(testing.BeForbiddenError())
			})

			ginkgo.By("Check that queue label not deleted from pod template spec", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					g.Expect(createdDeployment.Spec.Template.Labels).Should(gomega.HaveKey(constants.QueueLabel))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("shouldn't allow to change the queue name (ReadyReplicas > 0)", func() {
			createdDeployment := &appsv1.Deployment{}

			ginkgo.By("Update deployment status", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					createdDeployment.Status.Replicas = 1
					createdDeployment.Status.ReadyReplicas = 1
					g.Expect(k8sClient.Status().Update(ctx, createdDeployment)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Try to update", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					deploymentWrapper := &testingdeployment.DeploymentWrapper{Deployment: *createdDeployment}
					updatedDeployment := deploymentWrapper.Queue("another-queue").Obj()
					g.Expect(k8sClient.Update(ctx, updatedDeployment)).To(testing.BeForbiddenError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check queue name is injected to pod template label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					g.Expect(createdDeployment.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.Equal("user-queue"),
							"Queue name should be injected to pod template labels",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("the queue-name label is not set", func() {
		ginkgo.BeforeEach(func() {
			ginkgo.By("Create deployment", func() {
				deployment = testingdeployment.MakeDeployment("deployment", ns.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, deployment)).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not inject queue name to pod template labels", func() {
			createdDeployment := &appsv1.Deployment{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
				g.Expect(createdDeployment.Spec.Template.Labels[constants.QueueLabel]).
					To(
						gomega.BeEmpty(),
						"Queue name should not be injected to pod template labels",
					)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should allow to change the queue name", func() {
			createdDeployment := &appsv1.Deployment{}

			ginkgo.By("Change queue name", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					deploymentWrapper := &testingdeployment.DeploymentWrapper{Deployment: *createdDeployment}
					updatedDeployment := deploymentWrapper.Queue("user-queue").Obj()
					g.Expect(k8sClient.Update(ctx, updatedDeployment)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check queue name is injected to pod template label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deployment), createdDeployment)).Should(gomega.Succeed())
					g.Expect(createdDeployment.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.Equal("user-queue"),
							"Queue name should be injected to pod template labels",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
