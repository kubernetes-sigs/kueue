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

package v1alpha2

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var _ = ginkgo.Describe("ResourceFlavor Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a ResourceFlavor", func() {
		ginkgo.It("Should have a finalizer", func() {
			ginkgo.By("Creating a new resourceFlavor")
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).Should(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()

			var created kueue.ResourceFlavor
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &created)).Should(gomega.Succeed())
			gomega.Expect(created.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
		})
	})

	ginkgo.When("Creating a ResourceFlavor with invalid labels", func() {
		ginkgo.It("Should fail to create", func() {
			ginkgo.By("Creating a new resourceFlavor")
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Label("foo", "@abcd").Obj()
			err := k8sClient.Create(ctx, resourceFlavor)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring(errorInvalidLabelValue("@abcd")))
		})
	})

	ginkgo.When("Updating a ResourceFlavor with invalid labels", func() {
		ginkgo.It("Should fail to update", func() {
			ginkgo.By("Creating a new resourceFlavor")
			resourceFlavor := testing.MakeResourceFlavor("resource-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).Should(gomega.Succeed())
			defer func() {
				var rf kueue.ResourceFlavor
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &rf)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&rf, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &rf)).Should(gomega.Succeed())
				framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			}()

			var created kueue.ResourceFlavor
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(resourceFlavor), &created)).Should(gomega.Succeed())
			created.Labels = map[string]string{"foo": "@abcd"}

			ginkgo.By("Updating the resourceFlavor with invalid labels")
			err := k8sClient.Update(ctx, &created)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err.Error()).To(gomega.ContainSubstring(errorInvalidLabelValue("@abcd")))
		})
	})
})

func errorInvalidLabelValue(labelValue string) string {
	return fmt.Sprintf("ResourceFlavor.kueue.x-k8s.io \"resource-flavor\" is invalid: metadata.labels: Invalid value: \"%s\"", labelValue)
}
