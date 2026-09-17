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
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Deployment Webhook", func() {
	var (
		ns         *corev1.Namespace
		deployment *appsv1.Deployment
	)

	ginkgo.BeforeEach(func() {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
		err = serverVersionFetcher.FetchServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		fwk.StartManager(ctx, cfg, managerSetup(
			deploymentcontroller.SetupWebhook,
			jobframework.WithKubeServerVersion(serverVersionFetcher),
		))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "deployment-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.When("the queue-name label is set", func() {
		ginkgo.BeforeEach(func() {
			ginkgo.By("Create deployment", func() {
				deployment = testingdeployment.MakeDeployment("deployment", ns.Name).
					Queue("user-queue").
					Obj()
				util.MustCreate(ctx, k8sClient, deployment)
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
				gomega.Expect(k8sClient.Update(ctx, createdDeployment)).To(utiltesting.BeForbiddenError())
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
					g.Expect(k8sClient.Update(ctx, updatedDeployment)).To(utiltesting.BeForbiddenError())
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
				util.MustCreate(ctx, k8sClient, deployment)
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

	// Regression test for GC teardown deadlock (mirrors the StatefulSet and
	// LeaderWorkerSet specs): when foreground and background deletion are mixed
	// in the same ownership chain, a child Deployment can get stuck in
	// Terminating because the webhook denied the GC's PATCH to remove the
	// foregroundDeletion finalizer (parent was already gone).
	ginkgo.When("a child Deployment is terminating with its parent already deleted", func() {
		ginkgo.It("Should allow removing foregroundDeletion finalizer", func() {
			deployGVK := appsv1.SchemeGroupVersion.WithKind("Deployment")

			// Create the parent Deployment (no finalizers, so it is deleted immediately).
			parentDeployment := testingdeployment.MakeDeployment("parent-deployment", ns.Name).Queue("user-queue").Obj()
			util.MustCreate(ctx, k8sClient, parentDeployment)

			// Re-read to get the real UID assigned by the API server.
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentDeployment), parentDeployment)).To(gomega.Succeed())

			// Create the child Deployment with an ownerReference to the parent and
			// the foregroundDeletion finalizer (as the GC would add during teardown).
			childDeployment := testingdeployment.MakeDeployment("child-deployment", ns.Name).Obj()
			childDeployment.Finalizers = []string{metav1.FinalizerDeleteDependents}
			isController := true
			childDeployment.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: deployGVK.GroupVersion().String(),
					Kind:       deployGVK.Kind,
					Name:       parentDeployment.Name,
					UID:        parentDeployment.UID,
					Controller: &isController,
				},
			}
			util.MustCreate(ctx, k8sClient, childDeployment)

			// Delete the parent Deployment with background propagation: it has no
			// finalizers so it disappears from the API server immediately,
			// simulating the "background delete while foreground chain is active"
			// scenario described in the bug report.
			background := metav1.DeletePropagationBackground
			gomega.Expect(k8sClient.Delete(ctx, parentDeployment, &client.DeleteOptions{
				PropagationPolicy: &background,
			})).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentDeployment), &appsv1.Deployment{})).
					Should(gomega.MatchError(gomega.ContainSubstring("not found")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Delete the child Deployment so it enters Terminating state.
			// The foregroundDeletion finalizer prevents it from being fully removed.
			gomega.Expect(k8sClient.Delete(ctx, childDeployment)).To(gomega.Succeed())

			var terminatingChild appsv1.Deployment
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(childDeployment), &terminatingChild)).To(gomega.Succeed())
				g.Expect(terminatingChild.DeletionTimestamp).NotTo(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Simulate what the GC does: PATCH the child Deployment to remove the
			// foregroundDeletion finalizer.  Without the tolerance for deleting
			// objects this PATCH is denied by the mutating webhook with
			// "workload owner not found".
			patch := client.MergeFrom(terminatingChild.DeepCopy())
			terminatingChild.Finalizers = nil
			gomega.Expect(k8sClient.Patch(ctx, &terminatingChild, patch)).To(gomega.Succeed())
		})
	})
})
