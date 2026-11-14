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

package dynamicquotaorchestrator

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/core/localcapacity"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Local-capacity CapacityProvider node event filtering", ginkgo.Label("controller:localcapacity", "area:dynamicquotaorchestration"), func() {
	const (
		gpu       corev1.ResourceName = "nvidia.com/gpu"
		poolLabel string              = "example.com/pool"
	)

	var providerA, providerB *kueuealpha.CapacityProvider

	// gpuCapacity returns the GPU capacity published for the provider's only flavor.
	gpuCapacity := func(g gomega.Gomega, provider *kueuealpha.CapacityProvider) string {
		latest := &kueuealpha.CapacityProvider{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(provider), latest)).To(gomega.Succeed())
		g.Expect(latest.Status.Capacity).NotTo(gomega.BeNil())
		g.Expect(latest.Status.Capacity.Flavors).To(gomega.HaveLen(1))
		quantity := latest.Status.Capacity.Flavors[0].Resources[gpu]
		return quantity.String()
	}

	createNode := func(name, pool string) {
		n := testingnode.MakeNode(name).
			Label(poolLabel, pool).
			StatusAllocatable(corev1.ResourceList{gpu: resource.MustParse("8")}).
			Ready().
			Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*n})
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, n, true)
		})
	}

	ginkgo.BeforeEach(func() {
		for _, pool := range []string{"a", "b"} {
			flavor := utiltestingapi.MakeResourceFlavor("pool-"+pool).NodeLabel(poolLabel, pool).Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			})
		}
		providerA = utiltestingalpha.MakeCapacityProvider("pool-a").
			ControllerName(localcapacity.ControllerName).
			OrchestratedFlavors("pool-a").
			Obj()
		providerB = utiltestingalpha.MakeCapacityProvider("pool-b").
			ControllerName(localcapacity.ControllerName).
			OrchestratedFlavors("pool-b").
			Obj()
		for _, p := range []*kueuealpha.CapacityProvider{providerA, providerB} {
			behavioral.MustCreate(ctx, k8sClient, p)
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, p, true)
			})
		}
	})

	ginkgo.It("Should update only the providers whose flavors the Node matches", func() {
		ginkgo.By("Waiting for both providers to publish their initial capacity", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(gpuCapacity(g, providerA)).To(gomega.Equal("0"))
				g.Expect(gpuCapacity(g, providerB)).To(gomega.Equal("0"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Adding nodes to pool A updates provider A and leaves provider B unchanged", func() {
			createNode("filtering-a-1", "a")
			createNode("filtering-a-2", "a")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(gpuCapacity(g, providerA)).To(gomega.Equal("16"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(gpuCapacity(g, providerB)).To(gomega.Equal("0"))
			}, behavioral.ShortConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Moving a node from pool A to pool B updates both providers", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				n := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "filtering-a-2"}, n)).To(gomega.Succeed())
				n.Labels[poolLabel] = "b"
				g.Expect(k8sClient.Update(ctx, n)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(gpuCapacity(g, providerA)).To(gomega.Equal("8"))
				g.Expect(gpuCapacity(g, providerB)).To(gomega.Equal("8"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Removing the pool label from a node updates the provider that used to own it", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				n := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "filtering-a-2"}, n)).To(gomega.Succeed())
				delete(n.Labels, poolLabel)
				g.Expect(k8sClient.Update(ctx, n)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(gpuCapacity(g, providerB)).To(gomega.Equal("0"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
