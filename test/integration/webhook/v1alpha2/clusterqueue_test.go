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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var _ = ginkgo.Describe("ClusterQueue Webhook", func() {
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

	ginkgo.When("Creating a ClusterQueue", func() {
		ginkgo.It("Should have a finalizer", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			defer func() {
				var deleteCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &deleteCQ)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&deleteCQ, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &deleteCQ)).Should(gomega.Succeed())
				framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, &deleteCQ, true)
			}()

			var created kueue.ClusterQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &created)).Should(gomega.Succeed())
			gomega.Expect(created.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
		})

		ginkgo.It("Should have qualified resource names when creating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Resource(
				testing.MakeResource("@cpu").Obj(),
			).Obj()
			err := k8sClient.Create(ctx, cq)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should have qualified resource names when updating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			defer func() {
				var deleteCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &deleteCQ)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&deleteCQ, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &deleteCQ)).Should(gomega.Succeed())
				framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, &deleteCQ, true)
			}()

			var updateCQ kueue.ClusterQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
			updateCQ.Spec.Resources = []kueue.Resource{
				*testing.MakeResource("@cpu").Obj(),
			}

			err := k8sClient.Update(ctx, &updateCQ)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should have qualified flavor names when creating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Resource(
				testing.MakeResource("cpu").Flavor(testing.MakeFlavor("invalid_name", "5").Obj()).Obj(),
			).Obj()
			err := k8sClient.Create(ctx, cq)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should have qualified flavor names when creating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Resource(
				testing.MakeResource("cpu").Flavor(testing.MakeFlavor("x86", "5").Obj()).Obj(),
			).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			defer func() {
				var deleteCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &deleteCQ)).Should(gomega.Succeed())
				controllerutil.RemoveFinalizer(&deleteCQ, kueue.ResourceInUseFinalizerName)
				gomega.Expect(k8sClient.Update(ctx, &deleteCQ)).Should(gomega.Succeed())
				framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, &deleteCQ, true)
			}()

			var updateCQ kueue.ClusterQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updateCQ)).Should(gomega.Succeed())
			updateCQ.Spec.Resources[0].Flavors[0].Name = "invalid_name"

			err := k8sClient.Update(ctx, &updateCQ)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should have non-negative quota value when creating", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Resource(
				testing.MakeResource("cpu").Flavor(testing.MakeFlavor("x86", "-1").Obj()).Obj(),
			).Obj()
			err := k8sClient.Create(ctx, cq)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should have quota whose max value is greater than min", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").Resource(
				testing.MakeResource("cpu").Flavor(testing.MakeFlavor("x86", "2").Max("1").Obj()).Obj(),
			).Obj()
			err := k8sClient.Create(ctx, cq)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should forbid clusterQueue creation with unsupported scoringStrategy", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").QueueingStrategy("unknown").Obj()
			err := k8sClient.Create(ctx, cq)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsInvalid(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should forbid clusterQueue creation with unqualified labelSelector", func() {
			ginkgo.By("Creating a new clusterQueue")
			cq := testing.MakeClusterQueue("cluster-queue").NamespaceSelector(&metav1.LabelSelector{
				MatchLabels: map[string]string{"nospecialchars^=@": "bar"},
			}).Obj()
			err := k8sClient.Create(ctx, cq)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})
	})
})
