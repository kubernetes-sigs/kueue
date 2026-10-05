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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/localcapacity"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Local-capacity CapacityProvider controller", ginkgo.Label("controller:localcapacity", "area:dynamicquotaorchestration"), func() {
	const gpu corev1.ResourceName = "nvidia.com/gpu"

	var (
		flavor   *kueue.ResourceFlavor
		cohort   *kueue.Cohort
		teamCQ   *kueue.ClusterQueue
		provider *kueuealpha.CapacityProvider
		dqo      *kueuealpha.DynamicQuotaOrchestrator
		nodes    []*corev1.Node
	)

	makeNode := func(name string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label("example.com/gpu-type", "h100").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("120"),
				gpu:                resource.MustParse("8"),
			}).
			Ready().
			Obj()
	}

	createNodes := func(names ...string) {
		for _, name := range names {
			n := makeNode(name)
			behavioral.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*n})
			nodes = append(nodes, n)
		}
	}

	// effectiveGPUQuota returns the GPU nominal quota the scheduler uses for the object.
	effectiveGPUQuota := func(g gomega.Gomega, obj client.Object) string {
		g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(gomega.Succeed())
		var eq *kueue.EffectiveQuotaStatus
		switch o := obj.(type) {
		case *kueue.Cohort:
			eq = o.Status.EffectiveQuotas
		case *kueue.ClusterQueue:
			eq = o.Status.EffectiveQuotas
		}
		g.Expect(eq).NotTo(gomega.BeNil())
		var quota string
		found := false
		for _, rg := range eq.ResourceGroups {
			for _, fq := range rg.Flavors {
				if fq.Name != kueue.ResourceFlavorReference(flavor.Name) {
					continue
				}
				for _, rq := range fq.Resources {
					if rq.Name == gpu {
						quota, found = rq.NominalQuota.String(), true
						break
					}
				}
			}
		}
		g.Expect(found).To(gomega.BeTrue(), "gpu quota not found in effectiveQuotas")
		return quota
	}

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicQuotaOrchestration, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalCapacityProvider, true)

		flavor = utiltestingapi.MakeResourceFlavor("h100").NodeLabel("example.com/gpu-type", "h100").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		// The shared Cohort is the only participant with a positive weight, so it receives all capacity.
		cohort = utiltestingapi.MakeCohort("shared-pool").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("h100").Resource(gpu, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cohort)

		teamCQ = utiltestingapi.MakeClusterQueue("team-a").
			Cohort("shared-pool").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("h100").Resource(gpu, "0").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, teamCQ)

		provider = utiltestingalpha.MakeCapacityProvider("nodes").
			ControllerName(localcapacity.ControllerName).
			OrchestratedFlavors("h100").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, provider)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("nodes").
			DiscoveryProvider(provider.Name, nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "shared-pool").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, dqo)
	})

	ginkgo.AfterEach(func() {
		for _, n := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, n, true)
		}
		nodes = nil
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, provider, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, teamCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("Should make quota follow the eligible nodes", func() {
		ginkgo.By("Publishing the capacity of two ready nodes", func() {
			createNodes("h100-1", "h100-2")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("16"))
				g.Expect(effectiveGPUQuota(g, teamCQ)).To(gomega.Equal("0"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			latest := &kueuealpha.CapacityProvider{}
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(provider), latest)).To(gomega.Succeed())
			gomega.Expect(latest.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
				kueuealpha.CapacityProviderCapacitySynchronized, kueuealpha.CapacityProviderReasonSynchronized))
		})

		ginkgo.By("Adding a node increases quota", func() {
			createNodes("h100-3")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("24"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Cordoning a node decreases quota", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				n := &corev1.Node{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "h100-3"}, n)).To(gomega.Succeed())
				n.Spec.Unschedulable = true
				g.Expect(k8sClient.Update(ctx, n)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("16"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Removing all nodes drops quota to zero instead of falling back to spec", func() {
			for _, n := range nodes {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, n, true)
			}
			nodes = nil
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("0"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should keep the last good quota when the provider becomes Misconfigured", func() {
		ginkgo.By("Publishing the capacity of two ready nodes", func() {
			createNodes("h100-1", "h100-2")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("16"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		// A label-less flavor matches every node, so orchestrating it overlaps with h100.
		defaultFlavor := utiltestingapi.MakeResourceFlavor("default").Obj()
		ginkgo.By("Orchestrating an overlapping flavor", func() {
			behavioral.MustCreate(ctx, k8sClient, defaultFlavor)
			gomega.Eventually(func(g gomega.Gomega) {
				latest := &kueuealpha.CapacityProvider{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(provider), latest)).To(gomega.Succeed())
				latest.Spec.OrchestratedFlavors = append(latest.Spec.OrchestratedFlavors, kueuealpha.CapacityProviderOrchestratedFlavor{Name: "default"})
				g.Expect(k8sClient.Update(ctx, latest)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			})

			gomega.Eventually(func(g gomega.Gomega) {
				latest := &kueuealpha.CapacityProvider{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(provider), latest)).To(gomega.Succeed())
				g.Expect(latest.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.CapacityProviderCapacitySynchronized, kueuealpha.CapacityProviderReasonMisconfigured))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Adding a node does not change the quota while the provider is Misconfigured", func() {
			createNodes("h100-3")
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("16"))
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should recover when an overlapping provider is deleted", func() {
		createNodes("h100-1", "h100-2")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("16"))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		duplicate := utiltestingalpha.MakeCapacityProvider("nodes-duplicate").
			ControllerName(localcapacity.ControllerName).
			OrchestratedFlavors("h100").
			Obj()
		expectProviderReason := func(reason string) {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				latest := &kueuealpha.CapacityProvider{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(provider), latest)).To(gomega.Succeed())
				g.Expect(apimeta.FindStatusCondition(latest.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)).
					To(gomega.HaveField("Reason", reason))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.By("Creating a second provider for the same flavor", func() {
			behavioral.MustCreate(ctx, k8sClient, duplicate)
			expectProviderReason(kueuealpha.CapacityProviderReasonMisconfigured)
		})

		ginkgo.By("Deleting the second provider resolves the overlap", func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, duplicate, true)
			expectProviderReason(kueuealpha.CapacityProviderReasonSynchronized)
		})
	})

	ginkgo.It("Should apply effectiveCapacityMultiplier to the distributed quota", func() {
		createNodes("h100-1", "h100-2", "h100-3", "h100-4", "h100-5", "h100-6", "h100-7", "h100-8", "h100-9", "h100-10")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("80"))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Keeping 5% of the capacity as headroom", func() {
			multiplier := resource.MustParse("0.95")
			gomega.Eventually(func(g gomega.Gomega) {
				latest := &kueuealpha.DynamicQuotaOrchestrator{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dqo), latest)).To(gomega.Succeed())
				latest.Spec.CapacityDiscovery.Providers[0].EffectiveCapacityMultiplier = &multiplier
				g.Expect(k8sClient.Update(ctx, latest)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(effectiveGPUQuota(g, cohort)).To(gomega.Equal("76"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
