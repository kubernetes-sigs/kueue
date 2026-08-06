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
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("StatefulSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with pod integration enabled", func() {
		ginkgo.BeforeEach(func() {
			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			fwk.StartManager(ctx, cfg, managerSetup(
				statefulset.SetupWebhook,
				jobframework.WithManageJobsWithoutQueueName(false),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
			))
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "statefulset-")
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			fwk.StopManager(ctx)
		})

		ginkgo.When("The queue-name label is set", func() {
			ginkgo.It("Should inject SuspendedByParentAnnotation to pod template annotations", func() {
				sts := testingstatefulset.MakeStatefulSet("sts", ns.Name).Queue("user-queue").Obj()
				util.MustCreate(ctx, k8sClient, sts)

				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).Should(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Annotations[podconstants.SuspendedByParentAnnotation]).
						To(
							gomega.Equal("statefulset"),
							"SuspendedByParentAnnotation should be injected to pod template annotations",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("The queue-name label is not set", func() {
			ginkgo.It("Should not inject queue name to pod template labels", func() {
				sts := testingstatefulset.MakeStatefulSet("sts", ns.Name).Obj()
				util.MustCreate(ctx, k8sClient, sts)

				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).Should(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Labels[constants.QueueLabel]).
						To(
							gomega.BeEmpty(),
							"Queue name should not be injected to pod template labels",
						)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		// Regression test for GC teardown deadlock:
		// When foreground and background deletion are mixed in the same ownership chain,
		// a child STS can get stuck in Terminating because the webhook denied the GC's
		// PATCH to remove the foregroundDeletion finalizer (parent was already gone).
		ginkgo.When("a child StatefulSet is terminating with its parent already deleted", func() {
			ginkgo.It("Should allow removing foregroundDeletion finalizer", func() {
				stsGVK := appsv1.SchemeGroupVersion.WithKind("StatefulSet")

				// Create the parent STS (no finalizers, so it is deleted immediately).
				parentSTS := testingstatefulset.MakeStatefulSet("parent-sts", ns.Name).Queue("user-queue").Obj()
				util.MustCreate(ctx, k8sClient, parentSTS)

				// Re-read to get the real UID assigned by the API server.
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentSTS), parentSTS)).To(gomega.Succeed())

				// Create the child STS with an ownerReference to the parent and the
				// foregroundDeletion finalizer (as the GC would add during teardown).
				childSTS := testingstatefulset.MakeStatefulSet("child-sts", ns.Name).Obj()
				childSTS.Finalizers = []string{metav1.FinalizerDeleteDependents}
				isController := true
				childSTS.OwnerReferences = []metav1.OwnerReference{
					{
						APIVersion: stsGVK.GroupVersion().String(),
						Kind:       stsGVK.Kind,
						Name:       parentSTS.Name,
						UID:        parentSTS.UID,
						Controller: &isController,
					},
				}
				util.MustCreate(ctx, k8sClient, childSTS)

				// Delete the parent STS with background propagation: it has no
				// finalizers so it disappears from the API server immediately,
				// simulating the "background delete while foreground chain is active"
				// scenario described in the bug report.
				background := metav1.DeletePropagationBackground
				gomega.Expect(k8sClient.Delete(ctx, parentSTS, &client.DeleteOptions{
					PropagationPolicy: &background,
				})).To(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentSTS), &appsv1.StatefulSet{})).
						Should(gomega.MatchError(gomega.ContainSubstring("not found")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				// Delete the child STS so it enters Terminating state.
				// The foregroundDeletion finalizer prevents it from being fully removed.
				gomega.Expect(k8sClient.Delete(ctx, childSTS)).To(gomega.Succeed())

				var terminatingChild appsv1.StatefulSet
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(childSTS), &terminatingChild)).To(gomega.Succeed())
					g.Expect(terminatingChild.DeletionTimestamp).NotTo(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				// Simulate what the GC does: PATCH the child STS to remove the
				// foregroundDeletion finalizer.  Without the fix this PATCH is denied
				// by the mutating webhook with "workload owner not found".
				patch := client.MergeFrom(terminatingChild.DeepCopy())
				terminatingChild.Finalizers = nil
				gomega.Expect(k8sClient.Patch(ctx, &terminatingChild, patch)).To(gomega.Succeed())
			})
		})
	})
})
