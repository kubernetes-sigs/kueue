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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("LeaderWorkerSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(
			leaderworkerset.SetupWebhook,
			jobframework.WithManageJobsWithoutQueueName(false),
		))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lws-webhook-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.When("the LeaderWorkerSet is managed by Kueue", func() {
		ginkgo.It("should reject increasing the leaderWorkerTemplate size", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Queue("user-queue").
				Size(2).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			createdLws := &leaderworkersetv1.LeaderWorkerSet{}
			ginkgo.By("Increasing the size is rejected by the webhook", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
					createdLws.Spec.LeaderWorkerTemplate.Size = new(int32(10))
					g.Expect(k8sClient.Update(ctx, createdLws)).To(gomega.SatisfyAll(
						utiltesting.BeForbiddenError(),
						gomega.MatchError(gomega.ContainSubstring("spec.leaderWorkerTemplate.size")),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should reject decreasing the leaderWorkerTemplate size", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Queue("user-queue").
				Size(2).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			createdLws := &leaderworkersetv1.LeaderWorkerSet{}
			ginkgo.By("Decreasing the size is rejected by the webhook", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
					createdLws.Spec.LeaderWorkerTemplate.Size = new(int32(1))
					g.Expect(k8sClient.Update(ctx, createdLws)).To(gomega.SatisfyAll(
						utiltesting.BeForbiddenError(),
						gomega.MatchError(gomega.ContainSubstring("spec.leaderWorkerTemplate.size")),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow changing replicas", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Queue("user-queue").
				Replicas(1).
				Size(2).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			createdLws := &leaderworkersetv1.LeaderWorkerSet{}
			ginkgo.By("Increasing replicas is accepted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
					createdLws.Spec.Replicas = new(int32(3))
					g.Expect(k8sClient.Update(ctx, createdLws)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow LeaderWorkerSet with grouping and slicing", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Queue("user-queue").
				Size(5).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause",
							},
						},
					},
				}).
				LeaderTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
				LeaderTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				WorkerTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
				WorkerTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				WorkerTemplateSpecAnnotation(kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/rack").
				WorkerTemplateSpecAnnotation(kueue.PodSetSliceSizeAnnotation, "2").
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)
		})

		ginkgo.It("should allow LeaderWorkerSet with grouping and multi-layer slicing constraints", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws-multi-layer", ns.Name).
				Queue("user-queue").
				Size(5).
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause",
							},
						},
					},
				}).
				LeaderTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
				LeaderTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				WorkerTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
				WorkerTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				WorkerTemplateSpecAnnotation(kueue.PodSetSliceRequiredTopologyConstraintsAnnotation, `[{"topology":"cloud.com/rack","size":2}]`).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)
		})

		ginkgo.When("the TASGroupedPodSetSlicing feature gate is disabled", func() {
			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASGroupedPodSetSlicing, false)
			})

			ginkgo.It("should reject LeaderWorkerSet with grouping and slicing", func() {
				lws := testingleaderworkerset.MakeLeaderWorkerSet("lws-disabled", ns.Name).
					Queue("user-queue").
					Size(5).
					LeaderTemplate(corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name:  "c",
									Image: "pause",
								},
							},
						},
					}).
					LeaderTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
					LeaderTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					WorkerTemplateSpecAnnotation(kueue.PodSetGroupName, "test-group").
					WorkerTemplateSpecAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					WorkerTemplateSpecAnnotation(kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/rack").
					WorkerTemplateSpecAnnotation(kueue.PodSetSliceSizeAnnotation, "2").
					RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, lws)).Should(gomega.SatisfyAll(
					utiltesting.BeForbiddenError(),
					gomega.MatchError(gomega.ContainSubstring(kueue.PodSetGroupName)),
				))
			})
		})

		ginkgo.When("the LWSImmutableGroupSize feature gate is disabled", func() {
			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LWSImmutableGroupSize, false)
			})

			ginkgo.It("should allow increasing the leaderWorkerTemplate size", func() {
				lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
					Queue("user-queue").
					Size(2).
					RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
					Obj()
				util.MustCreate(ctx, k8sClient, lws)

				createdLws := &leaderworkersetv1.LeaderWorkerSet{}
				ginkgo.By("Increasing the size is accepted", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
						createdLws.Spec.LeaderWorkerTemplate.Size = new(int32(10))
						g.Expect(k8sClient.Update(ctx, createdLws)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})
	})

	ginkgo.When("the LeaderWorkerSet is not managed by Kueue", func() {
		ginkgo.It("should allow increasing the leaderWorkerTemplate size", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Size(2).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			createdLws := &leaderworkersetv1.LeaderWorkerSet{}
			ginkgo.By("Increasing the size is accepted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
					createdLws.Spec.LeaderWorkerTemplate.Size = new(int32(10))
					g.Expect(k8sClient.Update(ctx, createdLws)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	// Regression test for GC teardown deadlock:
	// When foreground and background deletion are mixed in the same ownership chain,
	// a child LeaderWorkerSet can get stuck in Terminating because the webhook denied
	// the GC's PATCH to remove the foregroundDeletion finalizer (parent was already gone).
	ginkgo.When("a child LeaderWorkerSet is terminating with its parent already deleted", func() {
		ginkgo.It("Should allow removing foregroundDeletion finalizer", func() {
			lwsGVK := leaderworkersetv1.SchemeGroupVersion.WithKind("LeaderWorkerSet")

			// Create the parent LWS (no finalizers, so it is deleted immediately).
			parentLWS := testingleaderworkerset.MakeLeaderWorkerSet("parent-lws", ns.Name).
				Queue("user-queue").
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, parentLWS)

			// Re-read to get the real UID assigned by the API server.
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentLWS), parentLWS)).To(gomega.Succeed())

			// Create the child LWS with an ownerReference to the parent and the
			// foregroundDeletion finalizer (as the GC would add during teardown).
			childLWS := testingleaderworkerset.MakeLeaderWorkerSet("child-lws", ns.Name).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			childLWS.Finalizers = []string{metav1.FinalizerDeleteDependents}
			isController := true
			childLWS.OwnerReferences = []metav1.OwnerReference{
				{
					APIVersion: lwsGVK.GroupVersion().String(),
					Kind:       lwsGVK.Kind,
					Name:       parentLWS.Name,
					UID:        parentLWS.UID,
					Controller: &isController,
				},
			}
			util.MustCreate(ctx, k8sClient, childLWS)

			// Delete the parent LWS with background propagation: it has no
			// finalizers so it disappears from the API server immediately,
			// simulating the "background delete while foreground chain is active"
			// scenario described in the bug report.
			background := metav1.DeletePropagationBackground
			gomega.Expect(k8sClient.Delete(ctx, parentLWS, &client.DeleteOptions{
				PropagationPolicy: &background,
			})).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentLWS), &leaderworkersetv1.LeaderWorkerSet{})).
					Should(gomega.MatchError(gomega.ContainSubstring("not found")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Delete the child LWS so it enters Terminating state.
			// The foregroundDeletion finalizer prevents it from being fully removed.
			gomega.Expect(k8sClient.Delete(ctx, childLWS)).To(gomega.Succeed())

			var terminatingChild leaderworkersetv1.LeaderWorkerSet
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(childLWS), &terminatingChild)).To(gomega.Succeed())
				g.Expect(terminatingChild.DeletionTimestamp).NotTo(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// Simulate what the GC does: PATCH the child LWS to remove the
			// foregroundDeletion finalizer.  Without the fix this PATCH is denied
			// by the mutating webhook with "workload owner not found".
			patch := client.MergeFrom(terminatingChild.DeepCopy())
			terminatingChild.Finalizers = nil
			gomega.Expect(k8sClient.Patch(ctx, &terminatingChild, patch)).To(gomega.Succeed())
		})
	})
})
