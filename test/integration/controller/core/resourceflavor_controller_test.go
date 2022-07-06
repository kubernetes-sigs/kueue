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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/core"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("ResourceFlavor controller", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-resourceflavor-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("no clusterQueue references resourceFlavors", func() {
		var resourceFlavor *kueue.ResourceFlavor
		ginkgo.BeforeEach(func() {
			resourceFlavor = utiltesting.MakeResourceFlavor("cq-refer-resourceflavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, resourceFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should have finalizer in new created resourceFlavor", func() {
			var rf kueue.ResourceFlavor
			gomega.Eventually(func() []string {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf); err != nil {
					return nil
				}
				return rf.GetFinalizers()
			}, framework.Timeout, framework.Interval).Should(utiltesting.Equal([]string{core.ResourceFlavorFinalizerName}))
		})

		ginkgo.It("Should handle resourceFlavor update events successfully", func() {
			var rf kueue.ResourceFlavor
			labels := map[string]string{"foo": "bar"}

			gomega.Eventually(func() map[string]string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				rf.Labels = labels
				if err := k8sClient.Update(ctx, &rf); err != nil {
					return nil
				}
				return rf.Labels
			}, framework.Timeout, framework.Interval).Should(utiltesting.Equal(labels))
			gomega.Expect(rf.GetDeletionTimestamp()).Should(gomega.BeNil())

			// TODO: When (issue#283) is resolved, remove the `Eventually` grammar.
			gomega.Eventually(func() []string {
				var newRF kueue.ResourceFlavor
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &newRF); err != nil {
					return nil
				}
				return newRF.GetFinalizers()
			}, framework.Timeout, framework.Interval).Should(utiltesting.Equal([]string{core.ResourceFlavorFinalizerName}))
		})
	})

	ginkgo.When("one clusterQueue references resourceFlavors", func() {
		var resourceFlavor *kueue.ResourceFlavor
		var clusterQueue *kueue.ClusterQueue
		var flavor *kueue.Flavor

		ginkgo.BeforeEach(func() {
			resourceFlavor = utiltesting.MakeResourceFlavor("cq-refer-resourceflavor").Obj()
			flavor = utiltesting.MakeFlavor(resourceFlavor.Name, "5").Obj()
			clusterQueue = utiltesting.MakeClusterQueue("foo").
				Resource(utiltesting.MakeResource("cpu").Flavor(flavor).Obj()).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		})

		ginkgo.It("Should delete the resourceFlavor when the corresponding clusterQueue no longer uses the resourceFlavor", func() {
			defer func() { gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed()) }()

			ginkgo.By("Try to delete resourceFlavor")
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, resourceFlavor)).To(gomega.Succeed())
			var rf kueue.ResourceFlavor
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				return rf.GetFinalizers()
			}, framework.Timeout, framework.Interval).Should(utiltesting.Equal([]string{core.ResourceFlavorFinalizerName}))
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			ginkgo.By("Update clusterQueue's cohort")
			var cq kueue.ClusterQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
			cq.Spec.Cohort = "foo-cohort"
			gomega.Expect(k8sClient.Update(ctx, &cq)).To(gomega.Succeed())
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)
			}, framework.Timeout, framework.Interval).Should(gomega.Succeed())

			ginkgo.By("Update clusterQueue's flavor")
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &cq)).To(gomega.Succeed())
			cq.Spec.Resources[0].Flavors[0].Name = "foo-resourceFlavor"
			gomega.Expect(k8sClient.Update(ctx, &cq)).To(gomega.Succeed())

			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)
			}, framework.Timeout, framework.Interval).Should(utiltesting.BeNotFoundError())
		})

		ginkgo.It("Should delete the resourceFlavor when the corresponding clusterQueue is deleted", func() {
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, resourceFlavor)).To(gomega.Succeed())

			var rf kueue.ResourceFlavor
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).To(gomega.Succeed())
				return rf.GetFinalizers()
			}, framework.Timeout, framework.Interval).Should(utiltesting.Equal([]string{core.ResourceFlavorFinalizerName}))
			gomega.Expect(rf.GetDeletionTimestamp()).ShouldNot(gomega.BeNil())

			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)
			}, framework.Timeout, framework.Interval).Should(utiltesting.BeNotFoundError())
		})
	})
})
