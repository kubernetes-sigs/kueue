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

package deployment

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Deployment Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with pod integration enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()

			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ctx, k8sClient = fwk.RunManager(cfg, managerSetup(
				nil,
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
				jobframework.WithIntegrationOptions(
					corev1.SchemeGroupVersion.WithKind("Pod").String(),
					&configapi.PodIntegrationOptions{},
				),
			))
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
		ginkgo.AfterAll(func() {
			fwk.Teardown()
		})

		ginkgo.When("The queue-name label is set", func() {
			var (
				deployment *appsv1.Deployment
				lookupKey  types.NamespacedName
			)

			ginkgo.BeforeEach(func() {
				deployment = testingdeployment.MakeDeployment("deployment-with-queue-name", ns.Name).
					Queue("user-queue").
					Obj()
				lookupKey = client.ObjectKeyFromObject(deployment)
			})

			ginkgo.It("Should inject queue name to pod template labels", func() {
				gomega.Expect(k8sClient.Create(ctx, deployment)).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) error {
					createdDeployment := &appsv1.Deployment{}
					g.Expect(k8sClient.Get(ctx, lookupKey, createdDeployment)).Should(gomega.Succeed())
					g.Expect(createdDeployment.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.Equal("user-queue"),
							"Queue name should be injected to pod template labels",
						)
					return nil
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Updating the deployment should not allow to change the queue name", func() {
					deploymentToUpdate := &appsv1.Deployment{}
					gomega.Expect(k8sClient.Get(ctx, lookupKey, deploymentToUpdate)).Should(gomega.Succeed())
					deploymentWrapper := &testingdeployment.DeploymentWrapper{
						Deployment: *deploymentToUpdate,
					}
					updatedDeployment := deploymentWrapper.Queue("another-queue").Obj()
					gomega.Expect(k8sClient.Update(ctx, updatedDeployment)).To(gomega.HaveOccurred())
				})
			})
		})
	})
})
