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
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("DynamicQuotaOrchestrator Validation", func() {
	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})
	ginkgo.AfterEach(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("Creating DynamicQuotaOrchestrator", func() {
		ginkgo.DescribeTable("Validate effectiveCapacityMultiplier in spec.capacityDiscovery.providers[*]",
			func(multiplier *resource.Quantity, isValid bool) {
				dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-mult").
					DiscoveryProvider("cp-1", multiplier).
					Obj()

				err := k8sClient.Create(ctx, dqo)
				if isValid {
					gomega.Expect(err).To(gomega.Succeed())
					defer func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
					}()
				} else {
					gomega.Expect(err).To(gomega.HaveOccurred())
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring("effectiveCapacityMultiplier must be non-negative"))
				}
			},
			ginkgo.Entry("Allow nil multiplier (defaults to 1)", nil, true),
			ginkgo.Entry("Allow zero integer multiplier", resource.NewQuantity(0, resource.DecimalSI), true),
			ginkgo.Entry("Allow positive integer multiplier", resource.NewQuantity(1, resource.DecimalSI), true),
			ginkgo.Entry("Allow positive fractional quantity multiplier", new(resource.MustParse("1.5")), true),
			ginkgo.Entry("Allow positive milli-quantity multiplier", new(resource.MustParse("500m")), true),
			ginkgo.Entry("Disallow negative integer multiplier", resource.NewQuantity(-1, resource.DecimalSI), false),
			ginkgo.Entry("Disallow negative quantity multiplier", new(resource.MustParse("-500m")), false),
		)

		ginkgo.It("Should disallow empty subtreeRootQuotaRef name", func() {
			dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty-name").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "").
				Obj()

			err := k8sClient.Create(ctx, dqo)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).To(utiltesting.BeInvalidError())
		})

		ginkgo.It("Should disallow empty providers", func() {
			dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty-providers").
				Obj()

			err := k8sClient.Create(ctx, dqo)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).To(utiltesting.BeInvalidError())
		})

		ginkgo.It("Should disallow provider with empty name", func() {
			dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-empty-provider-name").
				DiscoveryProvider("", nil).
				Obj()

			err := k8sClient.Create(ctx, dqo)
			gomega.Expect(err).To(gomega.HaveOccurred())
			gomega.Expect(err).To(utiltesting.BeInvalidError())
		})

		ginkgo.It("Should allow valid subtreeRootQuotaRef name", func() {
			dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-valid-name").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "valid-clusterqueue").
				Obj()

			behavioral.MustCreate(ctx, k8sClient, dqo)
			defer func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
			}()
		})
	})

	ginkgo.When("Updating DynamicQuotaOrchestrator status effective capacity flavors resources", func() {
		ginkgo.DescribeTable("Validate resources count in status.effectiveCapacity.flavors[*]",
			func(resourceCount int, isValid bool) {
				dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator(fmt.Sprintf("dqo-res-%d", resourceCount)).
					DiscoveryProvider("cp-1", nil).
					Obj()

				behavioral.MustCreate(ctx, k8sClient, dqo)
				defer func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
				}()

				resources := corev1.ResourceList{}
				for i := range resourceCount {
					resources[corev1.ResourceName(fmt.Sprintf("example.com/res-%d", i))] = resource.MustParse("1")
				}

				var fetched kueuealpha.DynamicQuotaOrchestrator
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dqo), &fetched)).To(gomega.Succeed())
				fetched.Status.EffectiveCapacity = &kueuealpha.EffectiveCapacity{
					Flavors: []kueuealpha.EffectiveCapacityFlavor{
						{
							Name:      "flavor-1",
							Resources: resources,
						},
					},
				}

				err := k8sClient.Status().Update(ctx, &fetched)
				if isValid {
					gomega.Expect(err).To(gomega.Succeed())
				} else {
					gomega.Expect(err).To(gomega.HaveOccurred())
					gomega.Expect(err).To(utiltesting.BeInvalidError())
					gomega.Expect(err.Error()).To(gomega.ContainSubstring("resource capacity must have at most 64 entries"))
				}
			},
			ginkgo.Entry("Allow empty resources (count 0)", 0, true),
			ginkgo.Entry("Allow minimum valid resources (count 1)", 1, true),
			ginkgo.Entry("Allow maximum valid resources (count 64)", 64, true),
			ginkgo.Entry("Disallow too many resources (count 65)", 65, false),
		)
	})

	ginkgo.When("Setting effectiveQuotaTTLSeconds", func() {
		ginkgo.It("Should allow effectiveQuotaTTLSeconds of 60 and 604800", func() {
			minTTL := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-ttl-min").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveQuotaTTL(time.Minute).
				Obj()
			maxTTL := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-ttl-max").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveQuotaTTL(7 * 24 * time.Hour).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, minTTL)
			behavioral.MustCreate(ctx, k8sClient, maxTTL)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, minTTL, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, maxTTL, true)
		})

		ginkgo.It("Should reject effectiveQuotaTTLSeconds of 59 and 604801", func() {
			belowMin := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-ttl-below-min").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveQuotaTTL(59 * time.Second).
				Obj()
			aboveMax := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-ttl-above-max").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				EffectiveQuotaTTL(7*24*time.Hour + time.Second).
				Obj()
			for _, dqo := range []*kueuealpha.DynamicQuotaOrchestrator{belowMin, aboveMax} {
				err := k8sClient.Create(ctx, dqo)
				gomega.Expect(err).To(utiltesting.BeInvalidError())
				gomega.Expect(err.Error()).To(gomega.ContainSubstring("spec.capacityDistribution.effectiveQuotaTTLSeconds"))
			}
		})

		ginkgo.It("Should leave effectiveQuotaTTLSeconds unset by default", func() {
			dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-ttl-default").
				DiscoveryProvider("cp-1", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq-1").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, dqo)
			defer func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
			}()

			var fetched kueuealpha.DynamicQuotaOrchestrator
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dqo), &fetched)).To(gomega.Succeed())
			gomega.Expect(fetched.Spec.CapacityDistribution.EffectiveQuotaTTLSeconds).To(gomega.BeNil())
		})
	})
})
