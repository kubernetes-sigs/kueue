/*
Copyright 2022 The Kubernetes Authors.

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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("ResourceFlavor controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{CRDPath: crdPath, WebhookPath: webhookPath}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup)
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-resourceflavor-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
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

			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			ginkgo.By("Wait for the queue to become active", func() {
				gomega.Eventually(func() bool {
					var cq kueue.ClusterQueue
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)
					if err != nil {
						return false
					}
					return apimeta.IsStatusConditionTrue(cq.Status.Conditions, kueue.ClusterQueueActive)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})
		})

		ginkgo.AfterEach(func() {
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should delete the resourceFlavor when the corresponding clusterQueue no longer uses the resourceFlavor", func() {
			ginkgo.By("Try to delete resourceFlavor")
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, resourceFlavor)).To(gomega.Succeed())
			var rf kueue.ResourceFlavor
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				return rf.GetFinalizers()
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			ginkgo.By("Update clusterQueue's cohort")
			var cq kueue.ClusterQueue
			gomega.Eventually(func() error {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
				cq.Spec.Cohort = "foo-cohort"
				return k8sClient.Update(ctx, &cq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Change clusterQueue's flavor")
			gomega.Eventually(func() error {
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)
				if err != nil {
					return err
				}
				cq.Spec.ResourceGroups[0].Flavors[0].Name = "alternate-flavor"
				return k8sClient.Update(ctx, &cq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)
			}, util.Timeout, util.Interval).Should(utiltesting.BeNotFoundError())
		})

		ginkgo.It("Should delete the resourceFlavor when the corresponding clusterQueue is deleted", func() {
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, resourceFlavor)).To(gomega.Succeed())

			var rf kueue.ResourceFlavor
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				return rf.GetFinalizers()
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]string{kueue.ResourceInUseFinalizerName}))
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)
			}, util.Timeout, util.Interval).Should(utiltesting.BeNotFoundError())
		})
	})
})
