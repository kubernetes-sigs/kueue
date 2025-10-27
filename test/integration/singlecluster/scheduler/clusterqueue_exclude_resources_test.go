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

package scheduler

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ClusterQueue ExcludeResourcePrefixes", func() {
	const (
		excludedPrefix1 = "example.com/"
		excludedPrefix2 = "foo.io/"
		excludedPrefix3 = "bar.io/"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ClusterQueueExcludeResources, true)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "cq-exclude-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, rf)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("ClusterQueue has excludeResourcePrefixes", func() {
		ginkgo.It("Should store and retrieve exclude resource prefixes", func() {
			cq := utiltestingapi.MakeClusterQueue("cq-with-exclusions").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).
				ExcludeResourcePrefixes([]string{excludedPrefix1, excludedPrefix2}).
				Obj()

			ginkgo.By("Verifying test object was created with prefixes")
			gomega.Expect(cq.Spec.ExcludeResourcePrefixes).To(gomega.ConsistOf(excludedPrefix1, excludedPrefix2))
			ginkgo.GinkgoWriter.Printf("Before create - CQ prefixes: %v\n", cq.Spec.ExcludeResourcePrefixes)

			util.MustCreate(ctx, k8sClient, cq)
			ginkgo.GinkgoWriter.Printf("After create - CQ prefixes: %v\n", cq.Spec.ExcludeResourcePrefixes)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			ginkgo.By("Checking the ClusterQueue has the correct exclude prefixes")
			var createdCQ kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
				ginkgo.GinkgoWriter.Printf("Retrieved CQ prefixes: %v\n", createdCQ.Spec.ExcludeResourcePrefixes)
				g.Expect(createdCQ.Spec.ExcludeResourcePrefixes).To(gomega.ConsistOf(excludedPrefix1, excludedPrefix2))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should update exclude resource prefixes", func() {
			cq := utiltestingapi.MakeClusterQueue("cq-update-exclusions").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).
				ExcludeResourcePrefixes([]string{excludedPrefix1}).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			ginkgo.By("Updating the ClusterQueue to change exclusions")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCQ)).To(gomega.Succeed())
				updatedCQ.Spec.ExcludeResourcePrefixes = []string{excludedPrefix2, excludedPrefix3}
				g.Expect(k8sClient.Update(ctx, &updatedCQ)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Checking the updated exclusions")
			var updatedCQ kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Spec.ExcludeResourcePrefixes).To(gomega.ConsistOf(excludedPrefix2, excludedPrefix3))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle empty exclude resource prefixes", func() {
			cq := utiltestingapi.MakeClusterQueue("cq-no-exclusions").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			ginkgo.By("Checking the ClusterQueue has no exclude prefixes")
			var createdCQ kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
				g.Expect(createdCQ.Spec.ExcludeResourcePrefixes).To(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Feature gate is disabled", func() {
		ginkgo.It("Should still accept exclude resource prefixes field", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ClusterQueueExcludeResources, false)

			cq := utiltestingapi.MakeClusterQueue("cq-feature-disabled").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).
				ExcludeResourcePrefixes([]string{excludedPrefix1}).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}()

			ginkgo.By("Checking the ClusterQueue was created with prefixes (field is always accepted)")
			var createdCQ kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
				g.Expect(createdCQ.Spec.ExcludeResourcePrefixes).To(gomega.ConsistOf(excludedPrefix1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
