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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ResourceFlavor controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-resourceflavor-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("one clusterQueue references resourceFlavors", func() {
		var resourceFlavor *kueue.ResourceFlavor
		var clusterQueue *kueue.ClusterQueue

		ginkgo.BeforeEach(func() {
			resourceFlavor = utiltesting.MakeResourceFlavor("flavor").Obj()
			clusterQueue = utiltesting.MakeClusterQueue("foo").
				ResourceGroup(*utiltesting.MakeFlavorQuotas("flavor").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()

			util.MustCreate(ctx, k8sClient, resourceFlavor)
			util.MustCreate(ctx, k8sClient, clusterQueue)

			ginkgo.By("Wait for the queue to become active", func() {
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
			})
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should delete the resourceFlavor when the corresponding clusterQueue no longer uses the resourceFlavor", func() {
			ginkgo.By("Try to delete resourceFlavor")
			gomega.Expect(util.DeleteObject(ctx, k8sClient, resourceFlavor)).To(gomega.Succeed())
			var rf kueue.ResourceFlavor
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				g.Expect(rf.GetFinalizers()).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			ginkgo.By("Update clusterQueue's cohort")
			var cq kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Spec.Cohort = "foo-cohort"
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Change clusterQueue's flavor")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).Should(gomega.Succeed())
				cq.Spec.ResourceGroups[0].Flavors[0].Name = "alternate-flavor"
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, false)
		})

		ginkgo.It("Should delete the resourceFlavor when the corresponding clusterQueue is deleted", func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, resourceFlavor)).To(gomega.Succeed())

			var rf kueue.ResourceFlavor
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				g.Expect(rf.GetFinalizers()).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			gomega.Expect(util.DeleteObject(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, false)
		})
	})
})
